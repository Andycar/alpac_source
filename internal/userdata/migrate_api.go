package userdata

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"lampac-go/internal/auth"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Types & constants
// ---------------------------------------------------------------------------

type migrationRecord struct {
	UserID     string    `json:"user_id"`
	OldUID     string    `json:"old_uid"`
	OldServer  string    `json:"old_server,omitempty"`
	MigratedAt time.Time `json:"migrated_at"`
	Bookmarks  bool      `json:"bookmarks"`
	Timecodes  bool      `json:"timecodes"`
	Storage    bool      `json:"storage"`
}

var (
	migrateMu        sync.Mutex
	reOldUID         = regexp.MustCompile(`^[a-z0-9]{6,16}$`)
	migrateRecordDir = filepath.Join("database", "migration")
	// migrateClient ходит на адрес, который ввёл ПОЛЬЗОВАТЕЛЬ, поэтому адрес проверяется при
	// соединении (migrateDialControl): validateServerURL видит только IP, записанный в URL
	// буквально, а имя вида 127.0.0.1.nip.io или DNS-ребиндинг пропускал — сервер можно было
	// заставить сходить в собственную внутреннюю сеть. Control срабатывает на уже разрешённом
	// адресе, то есть и после редиректа.
	migrateClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, Control: migrateDialControl}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("bad redirect scheme")
			}
			return nil
		},
	}

	// migrateAllowPrivate снимает проверку адреса при соединении — только для тестов (httptest
	// слушает 127.0.0.1).
	migrateAllowPrivate = false

	// Known storage paths used by sync v1 / v2 / backup.
	knownStoragePaths = []string{
		"sync_favorite", // v1 bookmarks
		"sync_view",     // v1+v2 watch state
		"backup",        // full backup
	}

	maxTimecodeCards  = 500
	remoteTimecodeSem = make(chan struct{}, 5) // concurrency limit
	maxRemoteRespSize = int64(10 << 20)        // 10 MB

	// validateServerURL is the server URL validator. Overridable in tests.
	validateServerURL = defaultValidateServerURL
)

// ---------------------------------------------------------------------------
// GET /migrate/check
// ---------------------------------------------------------------------------

func MigrateCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireTGUser(w, r)
		if !ok {
			return
		}

		oldUID := normalizeOldUID(r.URL.Query().Get("old_uid"))
		if oldUID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"available": false, "error": "invalid_old_uid"})
			return
		}

		profileSuffix := profileSuffixFromParam(r.URL.Query().Get("profile_id"))
		oldUserID := oldUID + profileSuffix
		newUserID := user.ID + profileSuffix

		if alreadyMigrated(newUserID, oldUID) {
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": "already_migrated"})
			return
		}

		oldServer := strings.TrimSpace(r.URL.Query().Get("old_server"))

		// Remote check
		if oldServer != "" {
			if err := validateServerURL(oldServer); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"available": false, "error": err.Error()})
				return
			}
			hasBookmarks := remoteHasBookmarks(oldServer, oldUID)
			writeJSON(w, http.StatusOK, map[string]any{
				"available": hasBookmarks,
				"mode":      "remote",
				"bookmarks": hasBookmarks,
				"timecodes": hasBookmarks, // if bookmarks exist, timecodes likely do too
				"storage":   true,
			})
			return
		}

		// Local check
		hasBookmarks := fileExists(bookmarkUserPath(oldUserID))
		hasTimecodes := fileExists(timecodeUserPath(oldUserID))
		hasStorage := localStorageExists(oldUserID)

		writeJSON(w, http.StatusOK, map[string]any{
			"available": hasBookmarks || hasTimecodes || hasStorage,
			"mode":      "local",
			"bookmarks": hasBookmarks,
			"timecodes": hasTimecodes,
			"storage":   hasStorage,
		})
	}
}

// ---------------------------------------------------------------------------
// POST /migrate/from-uid  (local migration — files on disk)
// ---------------------------------------------------------------------------

func MigrateFromUIDHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireTGUser(w, r)
		if !ok {
			return
		}

		var req struct {
			OldUID    string `json:"old_uid"`
			ProfileID string `json:"profile_id"`
		}
		if !readMigrateBody(w, r, &req) || req.OldUID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "old_uid_required"})
			return
		}

		oldUID := normalizeOldUID(req.OldUID)
		if oldUID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid_old_uid"})
			return
		}

		profileSuffix := profileSuffixFromParam(req.ProfileID)
		oldUserID := oldUID + profileSuffix
		newUserID := user.ID + profileSuffix

		if oldUserID == newUserID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "same_uid"})
			return
		}

		migrateMu.Lock()
		defer migrateMu.Unlock()

		if alreadyMigrated(newUserID, oldUID) {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "already_migrated"})
			return
		}

		// Merge all data types
		mBookmarks := mergeLocalBookmarks(oldUserID, newUserID)
		mTimecodes := mergeLocalTimecodes(oldUserID, newUserID)
		mStorage := migrateLocalStorage(oldUserID, newUserID)

		if !mBookmarks && !mTimecodes && !mStorage {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "no_old_data"})
			return
		}

		saveMigrationRecord(migrationRecord{
			UserID: newUserID, OldUID: oldUID,
			MigratedAt: time.Now().UTC(),
			Bookmarks:  mBookmarks, Timecodes: mTimecodes, Storage: mStorage,
		})

		log.Info().Str("old_uid", oldUID).Str("new_uid", newUserID).
			Bool("bookmarks", mBookmarks).Bool("timecodes", mTimecodes).Bool("storage", mStorage).
			Msg("migrate: local migration completed")

		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "bookmarks": mBookmarks, "timecodes": mTimecodes, "storage": mStorage,
		})
	}
}

// ---------------------------------------------------------------------------
// POST /migrate/from-server  (remote migration — fetch from old lampac API)
// ---------------------------------------------------------------------------

func MigrateFromServerHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireTGUser(w, r)
		if !ok {
			return
		}

		var req struct {
			OldServer string `json:"old_server"`
			OldUID    string `json:"old_uid"`
			ProfileID string `json:"profile_id"`
		}
		if !readMigrateBody(w, r, &req) {
			return
		}

		oldUID := normalizeOldUID(req.OldUID)
		if oldUID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "old_uid_required"})
			return
		}
		if err := validateServerURL(req.OldServer); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
			return
		}

		profileSuffix := profileSuffixFromParam(req.ProfileID)
		newUserID := user.ID + profileSuffix
		oldServer := strings.TrimRight(req.OldServer, "/")

		migrateMu.Lock()
		defer migrateMu.Unlock()

		if alreadyMigrated(newUserID, oldUID) {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "already_migrated"})
			return
		}

		// 1. Fetch & merge bookmarks
		mBookmarks, _ := remoteImportBookmarks(oldServer, oldUID, newUserID)

		// 2. Fetch & merge timecodes (card IDs from bookmarks)
		mTimecodes := remoteImportTimecodes(oldServer, oldUID, newUserID)

		// 3. Fetch & merge storage paths (sync v1/v2 + backup)
		mStorage := remoteImportStorage(oldServer, oldUID, newUserID)

		if !mBookmarks && !mTimecodes && !mStorage {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "no_old_data"})
			return
		}

		saveMigrationRecord(migrationRecord{
			UserID: newUserID, OldUID: oldUID, OldServer: oldServer,
			MigratedAt: time.Now().UTC(),
			Bookmarks:  mBookmarks, Timecodes: mTimecodes, Storage: mStorage,
		})

		log.Info().Str("old_server", oldServer).Str("old_uid", oldUID).Str("new_uid", newUserID).
			Bool("bookmarks", mBookmarks).Bool("timecodes", mTimecodes).Bool("storage", mStorage).
			Msg("migrate: remote migration completed")

		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "bookmarks": mBookmarks, "timecodes": mTimecodes, "storage": mStorage,
		})
	}
}

// ---------------------------------------------------------------------------
// Local merge functions
// ---------------------------------------------------------------------------

func mergeLocalBookmarks(oldUserID, newUserID string) bool {
	oldData, oldExists, _ := loadBookmarkUser(oldUserID)
	if !oldExists {
		return false
	}
	return mergeBookmarksFromData(newUserID, oldData)
}

func mergeLocalTimecodes(oldUserID, newUserID string) bool {
	oldStore, err := loadTimecodeUser(oldUserID)
	if err != nil || len(oldStore) == 0 {
		return false
	}
	return mergeTimecodesFromData(newUserID, oldStore)
}

func migrateLocalStorage(oldUserID, newUserID string) bool {
	opts := loadStorageOptions()
	storageBase := relToRuntime(filepath.Join("database", "storage"))

	entries, err := os.ReadDir(storageBase)
	if err != nil {
		return false
	}

	migrated := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pathName := entry.Name()

		oldPath, oldOK := resolveStoragePath(pathName, "", false, oldUserID, opts)
		if !oldOK || !fileExists(oldPath.fs) {
			continue
		}
		newPath, newOK := resolveStoragePath(pathName, "", true, newUserID, opts)
		if !newOK {
			continue
		}
		// Don't overwrite existing data
		if fileExists(newPath.fs) {
			continue
		}
		data, err := os.ReadFile(oldPath.fs)
		if err != nil || len(data) == 0 {
			continue
		}
		if os.WriteFile(newPath.fs, data, 0o644) == nil {
			migrated = true
		}
	}
	return migrated
}

// ---------------------------------------------------------------------------
// Remote fetch functions
// ---------------------------------------------------------------------------

// remoteImportBookmarks — закладки со старого сервера в Лампу-сторону нового пользователя. Второе
// значение — сами закладки (объект «favorite»), чтобы переложить их и в списки ALPAC.
func remoteImportBookmarks(oldServer, oldUID, newUserID string) (bool, map[string]any) {
	// Fetch v2 bookmarks from /bookmark/list
	u := oldServer + "/bookmark/list?uid=" + url.QueryEscape(oldUID)
	data, err := remoteGet(u)
	if err != nil || len(data) == 0 {
		return false, nil
	}

	var bookmarkData map[string]any
	if stdjson.Unmarshal(data, &bookmarkData) != nil {
		return false, nil
	}

	// Check if it's "not initialized" or empty
	if _, notInit := bookmarkData["dbInNotInitialization"]; notInit {
		return false, nil
	}

	return mergeBookmarksFromData(newUserID, bookmarkData), bookmarkData
}

func remoteImportTimecodes(oldServer, oldUID, newUserID string) bool {
	// Get card IDs from current user's bookmarks (just merged)
	newData, exists, _ := loadBookmarkUser(newUserID)
	if !exists {
		return false
	}

	cards, _ := newData["card"].([]any)
	if len(cards) == 0 {
		return false
	}

	// Collect unique card IDs
	cardIDs := make([]string, 0, len(cards))
	seen := make(map[string]bool, len(cards))
	for _, card := range cards {
		obj, ok := card.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(toString(obj["id"]))
		if id != "" && !seen[id] {
			seen[id] = true
			cardIDs = append(cardIDs, id)
		}
	}

	if len(cardIDs) > maxTimecodeCards {
		cardIDs = cardIDs[:maxTimecodeCards]
	}

	// Fetch timecodes per card with concurrency limit
	type tcResult struct {
		cardID string
		data   map[string]string
	}

	results := make(chan tcResult, len(cardIDs))
	var wg sync.WaitGroup

	for _, cid := range cardIDs {
		wg.Add(1)
		go func(cardID string) {
			defer wg.Done()
			remoteTimecodeSem <- struct{}{}
			defer func() { <-remoteTimecodeSem }()

			u := oldServer + "/timecode/all?card_id=" + url.QueryEscape(cardID) + "&uid=" + url.QueryEscape(oldUID)
			body, err := remoteGet(u)
			if err != nil || len(body) == 0 {
				return
			}
			var td map[string]string
			if stdjson.Unmarshal(body, &td) != nil || len(td) == 0 {
				return
			}
			results <- tcResult{cardID: cardID, data: td}
		}(cid)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	oldStore := make(timecodeUserData)
	for res := range results {
		oldStore[res.cardID] = res.data
	}

	if len(oldStore) == 0 {
		return false
	}
	return mergeTimecodesFromData(newUserID, oldStore)
}

func remoteImportStorage(oldServer, oldUID, newUserID string) bool {
	opts := loadStorageOptions()
	imported := false

	for _, pathName := range knownStoragePaths {
		u := oldServer + "/storage/get?path=" + url.QueryEscape(pathName) + "&uid=" + url.QueryEscape(oldUID)
		body, err := remoteGet(u)
		if err != nil || len(body) == 0 {
			continue
		}

		var resp struct {
			Success  bool   `json:"success"`
			Data     string `json:"data"`
			FileInfo any    `json:"fileInfo"`
		}
		if stdjson.Unmarshal(body, &resp) != nil || !resp.Success || resp.Data == "" {
			continue
		}

		// Save data to local storage for new user
		if storageSetForUser(newUserID, pathName, []byte(resp.Data), opts) == nil {
			imported = true
		}
	}
	return imported
}

func remoteHasBookmarks(oldServer, oldUID string) bool {
	u := oldServer + "/bookmark/list?uid=" + url.QueryEscape(oldUID)
	data, err := remoteGet(u)
	if err != nil || len(data) == 0 {
		return false
	}
	var resp map[string]any
	if stdjson.Unmarshal(data, &resp) != nil {
		return false
	}
	_, notInit := resp["dbInNotInitialization"]
	if notInit {
		return false
	}
	cards, _ := resp["card"].([]any)
	return len(cards) > 0
}

func remoteGet(rawURL string) ([]byte, error) {
	return remoteGetCtx(context.Background(), rawURL)
}

// remoteGetCtx — remoteGet с общим сроком на весь перенос: клиент ALPAC ждёт ответа, и девять
// попыток по 30 с к зависшему серверу он не переживёт.
func remoteGetCtx(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := migrateClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxRemoteRespSize))
}

// ---------------------------------------------------------------------------
// Shared merge logic (used by both local and remote)
// ---------------------------------------------------------------------------

func mergeBookmarksFromData(newUserID string, oldData map[string]any) bool {
	if len(oldData) == 0 {
		return false
	}

	bookmarkMu.Lock()
	defer bookmarkMu.Unlock()

	newData, _, _ := loadBookmarkUser(newUserID)
	changed := false

	// Merge cards. ensureBookmarkCard/addBookmarkCategoryID кладут в НАЧАЛО списка, поэтому идём
	// с конца: иначе перенесённые закладки вставали в обратном порядке — самые старые сверху.
	if oldCards, ok := oldData["card"].([]any); ok {
		for i := len(oldCards) - 1; i >= 0; i-- {
			card := oldCards[i]
			cardObj, ok := card.(map[string]any)
			if !ok {
				continue
			}
			cardID := strings.TrimSpace(strings.ToLower(toString(cardObj["id"])))
			if cardID != "" {
				if ensureBookmarkCard(newData, cardObj, cardID) {
					changed = true
				}
			}
		}
	}

	// Merge categories
	for _, cat := range bookmarkCategories {
		if oldIDs, ok := oldData[cat].([]any); ok {
			for i := len(oldIDs) - 1; i >= 0; i-- {
				idStr := toString(oldIDs[i])
				if idStr != "" {
					if addBookmarkCategoryID(newData, cat, idStr) {
						changed = true
					}
				}
			}
		}
	}

	if !changed {
		return false
	}

	ensureBookmarkDefaults(newData)
	return saveBookmarkUser(newUserID, newData) == nil
}

func mergeTimecodesFromData(newUserID string, oldStore timecodeUserData) bool {
	if len(oldStore) == 0 {
		return false
	}

	timecodeMu.Lock()
	defer timecodeMu.Unlock()

	newStore, _ := loadTimecodeUser(newUserID)
	if newStore == nil {
		newStore = make(timecodeUserData)
	}

	changed := false
	for cardID, oldEntries := range oldStore {
		if newStore[cardID] == nil {
			// No new data for this card — take all old entries
			newStore[cardID] = oldEntries
			changed = true
		} else {
			// Merge: old entries fill gaps, new entries win on conflict
			for key, val := range oldEntries {
				if _, exists := newStore[cardID][key]; !exists {
					newStore[cardID][key] = val
					changed = true
				}
			}
		}
	}

	if !changed {
		return false
	}
	return saveTimecodeUser(newUserID, newStore) == nil
}

func storageSetForUser(userUID, pathName string, data []byte, opts storageOptions) error {
	out, ok := resolveStoragePath(pathName, "", true, userUID, opts)
	if !ok {
		return os.ErrInvalid
	}
	// Don't overwrite existing data
	if fileExists(out.fs) {
		return nil
	}
	return os.WriteFile(out.fs, data, 0o644)
}

// ---------------------------------------------------------------------------
// Migration records (replay protection)
// ---------------------------------------------------------------------------

func alreadyMigrated(newUserID, oldUID string) bool {
	for _, r := range loadMigrationRecords() {
		if r.UserID == newUserID && r.OldUID == oldUID {
			return true
		}
	}
	return false
}

func loadMigrationRecords() []migrationRecord {
	path := relToRuntime(filepath.Join(migrateRecordDir, "done.json"))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var records []migrationRecord
	_ = stdjson.Unmarshal(data, &records)
	return records
}

func saveMigrationRecord(rec migrationRecord) {
	records := loadMigrationRecords()
	records = append(records, rec)
	path := relToRuntime(filepath.Join(migrateRecordDir, "done.json"))
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	data, _ := stdjson.MarshalIndent(records, "", "  ")
	_ = os.WriteFile(path, data, 0o644)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func requireTGUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil || !strings.HasPrefix(user.ID, "tg:") {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "tg_auth_required"})
		return nil, false
	}
	return user, true
}

func normalizeOldUID(raw string) string {
	uid := strings.TrimSpace(strings.ToLower(raw))
	if !reOldUID.MatchString(uid) {
		return ""
	}
	return uid
}

func profileSuffixFromParam(profileID string) string {
	pid := strings.TrimSpace(profileID)
	if pid != "" && pid != "0" {
		return "_" + pid
	}
	return ""
}

func readMigrateBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
		return false
	}
	if stdjson.Unmarshal(body, dst) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_json"})
		return false
	}
	return true
}

func defaultValidateServerURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &url.Error{Op: "validate", URL: raw, Err: net.UnknownNetworkError("empty url")}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return err
	}

	// Only allow http/https
	if u.Scheme != "http" && u.Scheme != "https" {
		return &url.Error{Op: "validate", URL: raw, Err: net.UnknownNetworkError("invalid scheme")}
	}

	host := u.Hostname()

	// SSRF protection: block private/loopback IPs
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return &url.Error{Op: "validate", URL: raw, Err: net.UnknownNetworkError("private_ip_blocked")}
		}
	}
	lower := strings.ToLower(host)
	if lower == "localhost" {
		return &url.Error{Op: "validate", URL: raw, Err: net.UnknownNetworkError("localhost_blocked")}
	}

	return nil
}

func localStorageExists(oldUserID string) bool {
	opts := loadStorageOptions()
	for _, pathName := range knownStoragePaths {
		out, ok := resolveStoragePath(pathName, "", false, oldUserID, opts)
		if ok && fileExists(out.fs) {
			return true
		}
	}
	return false
}

// fileExists is defined in admin_manifest.go

var errBlockedAddress = errors.New("private_ip_blocked")

func migrateDialControl(_, address string, _ syscall.RawConn) error {
	if migrateAllowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || blockedRemoteIP(ip) {
		return errBlockedAddress
	}
	return nil
}

// blockedRemoteIP — адреса, куда сервер по просьбе пользователя не ходит: своя машина, частные
// сети, link-local, CGNAT и служебные диапазоны.
func blockedRemoteIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0: // 0.0.0.0/8
			return true
		case v4[0] == 100 && v4[1]&0xC0 == 64: // 100.64.0.0/10 (CGNAT)
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return true
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18.0.0/15
			return true
		}
	}
	return false
}
