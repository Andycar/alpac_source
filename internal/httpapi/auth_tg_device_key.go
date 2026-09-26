package httpapi

// Регистрация аппаратного ключа устройства (аттестация v3).
//
// Зачем это поверх уже существующего v2. Ключ v2 (proof_key) — обычная строка в
// SharedPreferences, то есть файл в каталоге данных приложения. Разобранный
// 2026-09-11 мод ничего не ломал: он положил внутрь APK zip со слепком этого
// каталога — токен, uid и proof_key — и распаковывал его при первом запуске.
// Аттестация отрабатывала честно, потому что ключ настоящий; просто устройств с
// одним ключом стало много.
//
// Ключ v3 создаётся ВНУТРИ AndroidKeyStore устройства. Приватной части у
// приложения нет: API не умеет её экспортировать, а материал лежит в
// /data/misc/keystore (с TEE — в отдельном сопроцессоре), вне каталога
// приложения. Сюда приезжает только ПУБЛИЧНАЯ часть, и скопированная установка
// подписать ею ничего не может.

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/tgauth"
)

// maxPubKeyLen ограничивает тело: SubjectPublicKeyInfo для P-256 — около 120
// байт в DER, с запасом на другие кривые берём 1 КБ.
const maxPubKeyLen = 1024

// tgAuthDeviceKeyHandler принимает публичный ключ устройства.
// POST /tg/auth/device-key  {"uid": "...", "pub": "<base64 DER>", "hardware": true}
//
// Токен берётся из куки/заголовка/квери, как везде. Регистрируется ключ ТОЛЬКО
// для устройства, уже привязанного к этому токену: иначе кто угодно объявил бы
// своим произвольный чужой uid.
func tgAuthDeviceKeyHandler(store *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no store"})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxPubKeyLen*4))
		var req struct {
			UID      string `json:"uid"`
			Pub      string `json:"pub"`
			Hardware bool   `json:"hardware"`
		}
		if stdjson.Unmarshal(body, &req) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
			return
		}
		req.UID = strings.TrimSpace(req.UID)
		req.Pub = strings.TrimSpace(req.Pub)
		if req.UID == "" || req.Pub == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "uid and pub required"})
			return
		}
		if len(req.Pub) > maxPubKeyLen {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "pub too long"})
			return
		}
		// Разбираем ключ ЗДЕСЬ, на регистрации, а не при каждой проверке подписи:
		// мусор не должен попадать в хранилище и молча ломать аттестацию потом.
		if !validPubKey(req.Pub) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad public key"})
			return
		}

		var token string
		for _, cand := range collectLampacTokenCandidates(r) {
			if store.HasDevice(cand, req.UID) {
				token = cand
				break
			}
		}
		if token == "" {
			// Либо токена нет, либо это устройство ему не принадлежит. Разницу
			// наружу не раскрываем — она подсказывала бы, какие uid существуют.
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "device not bound"})
			return
		}

		ok, err := store.SetDevicePubKey(token, req.UID, req.Pub, req.Hardware)
		if errors.Is(err, tgauth.ErrPubKeyAlreadySet) {
			// У устройства уже есть ДРУГОЙ ключ. Это либо переустановка (Keystore
			// чистится вместе с приложением), либо попытка подменить чужой ключ
			// своим. Разрешать перезапись нельзя: она свела бы v3 обратно к v2.
			// Лечится отвязкой устройства — как и всё остальное.
			log.Warn().Str("uid", req.UID).Str("ip", clientIP(r)).
				Msg("device-key: у устройства уже другой ключ, перезапись запрещена")
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "key already set"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "device not bound"})
			return
		}
		log.Info().Str("uid", req.UID).Bool("hardware", req.Hardware).Str("ip", clientIP(r)).
			Msg("device-key: зарегистрирован аппаратный ключ устройства")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hardware": req.Hardware})
	}
}

// validPubKey проверяет, что строка — это base64 от DER SubjectPublicKeyInfo с
// ECDSA-ключом. Чужие форматы отбиваем на входе.
func validPubKey(pubB64 string) bool {
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(der) == 0 {
		return false
	}
	return parseECDSAPub(der)
}

// parseECDSAPub отвечает, разбирается ли DER как ECDSA SubjectPublicKeyInfo.
func parseECDSAPub(der []byte) bool {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}
	_, ok := pub.(*ecdsa.PublicKey)
	return ok
}
