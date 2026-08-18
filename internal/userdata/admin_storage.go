package userdata

import (
	"context"
	"net/http"
	"os"
	"strings"
)

// Admin seam over the storage layout for the admin panel's «Бэкапы» section.
//
// The on-disk key is md5(userUID+pathfile) — one-way, so there is no way to walk
// database/storage back to owners. Attribution therefore goes forward: the admin
// handler supplies every identity the user could have written under (auth id
// "tg:<id>", every bound device UID for the legacy ?uid= plugins, custom
// backup_sync_key uids if known) plus the user's profile ids (syncpro sends the
// active profile as pathfile), and we stat the derived paths.

// AdminStoragePaths — every storage path the Lampa plugins write user state to.
// Raw (unsanitized) names; resolveStoragePath strips the underscores itself.
var AdminStoragePaths = []string{"backup", "sync_view", "sync_favorite", "sync_torrents", "search_history", "sync_plugins"}

// AdminStorageFile is one attributed blob.
type AdminStorageFile struct {
	Path     string `json:"path"`      // raw storage path name (backup, sync_view, …)
	PathFile string `json:"path_file"` // profile id suffix, "" = базовый
	OwnerUID string `json:"owner_uid"` // the identity the key was derived from
	Size     int64  `json:"size"`
	ModTime  int64  `json:"mtime"` // unix ms

	fs string // filesystem path — kept internal, deletion goes through AdminDeleteUserStorage
}

// AdminListUserStorage stats every (path × ownerUID × pathfile) combination and
// returns the blobs that exist. Misses are cheap (one os.Stat each).
func AdminListUserStorage(ownerUIDs []string, profileIDs []string) []AdminStorageFile {
	opts := loadStorageOptions()
	pathfiles := append([]string{""}, profileIDs...)
	out := []AdminStorageFile{}
	seen := map[string]bool{} // two identities can collide only if equal strings — dedup by fs path anyway
	for _, p := range AdminStoragePaths {
		for _, uid := range ownerUIDs {
			if uid == "" {
				continue
			}
			for _, pf := range pathfiles {
				sp, ok := resolveStoragePath(p, pf, false, uid, opts)
				if !ok || seen[sp.fs] {
					continue
				}
				st, err := os.Stat(sp.fs)
				if err != nil || st.IsDir() {
					continue
				}
				seen[sp.fs] = true
				out = append(out, AdminStorageFile{
					Path:     p,
					PathFile: pf,
					OwnerUID: uid,
					Size:     st.Size(),
					ModTime:  st.ModTime().UTC().UnixMilli(),
					fs:       sp.fs,
				})
			}
		}
	}
	return out
}

// AdminDeleteUserStorage removes the user's blobs for the given raw path names
// (nil/empty = all of AdminStoragePaths). Returns how many files were removed
// and how many bytes they held.
func AdminDeleteUserStorage(ownerUIDs []string, profileIDs []string, paths []string) (deleted int, freed int64) {
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	for _, f := range AdminListUserStorage(ownerUIDs, profileIDs) {
		if len(want) > 0 && !want[f.Path] {
			continue
		}
		if err := os.Remove(f.fs); err == nil {
			deleted++
			freed += f.Size
		}
	}
	return deleted, freed
}

// StorageWipeHandler — self-service: «снести мои бэкапы/настройки со всех серверов»
// одной кнопкой из клиента (syncpro → Действия). POST /storage/wipe.
//
// Личность — та же цепочка, что у всех /storage/* (авторизованный user.ID, иначе
// легаси ?uid=): удалять юзер может ровно те файлы, которые и так мог бы слепо
// перезаписать через /storage/set. `expand` (опционально) добавляет device-UID'ы
// и профили владельца — их знает хост (tgauth/capistore), не этот пакет; `fanout`
// (опционально) повторяет удаление на кластер-нодах и возвращает по-нодовые
// результаты. IP-фолбэк storageUserUID для деструктивной операции отрезан.
func StorageWipeHandler(
	expand func(userUID string) (extraOwners []string, profileIDs []string),
	fanout func(ctx context.Context, owners, profiles, paths []string) []map[string]any,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		opts := loadStorageOptions()
		if !opts.Enable {
			writeStorageError(w, http.StatusOK, "disabled")
			return
		}
		uid := storageUserUID(r)
		// storageUserUID падает до client-IP, когда нет ни авторизации, ни ?uid= —
		// для чтения это терпимо, для удаления нет: за NAT один IP = много людей.
		if uid == "" || (uid == deps.ClientIP(r) && !hasExplicitUID(r)) {
			writeStorageError(w, http.StatusOK, "no identity")
			return
		}
		owners := []string{uid}
		var profiles []string
		if expand != nil {
			extra, profs := expand(uid)
			owners = append(owners, extra...)
			profiles = profs
		}
		var paths []string
		if raw := strings.TrimSpace(r.URL.Query().Get("paths")); raw != "" {
			for _, p := range strings.Split(raw, ",") {
				if p = strings.TrimSpace(p); p != "" {
					paths = append(paths, p)
				}
			}
		}
		deleted, freed := AdminDeleteUserStorage(owners, profiles, paths)
		resp := map[string]any{"success": true, "deleted": deleted, "freed": freed}
		if fanout != nil {
			nodes := fanout(r.Context(), owners, profiles, paths)
			remote := 0
			for _, n := range nodes {
				if respMap, ok := n["response"].(map[string]any); ok {
					if d, ok := respMap["deleted"].(float64); ok {
						remote += int(d)
					}
				}
			}
			resp["nodes"] = len(nodes)
			resp["deleted_remote"] = remote
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// hasExplicitUID: запрос сам назвал владельца (?uid=/заголовок/кука) — в отличие
// от чистого IP-фолбэка.
func hasExplicitUID(r *http.Request) bool {
	q := r.URL.Query()
	if q.Get("uid") != "" || q.Get("user_uid") != "" || q.Get("id") != "" || r.Header.Get("X-User-Uid") != "" {
		return true
	}
	if c, err := r.Cookie("uid"); err == nil && c.Value != "" {
		return true
	}
	return false
}
