package tgauth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// ErrPubKeyAlreadySet — у устройства уже есть публичный ключ, и он другой.
// Перезапись запрещена: иначе укравший токен подменил бы чужой ключ своим.
var ErrPubKeyAlreadySet = errors.New("device already has a public key")

// proofkey.go — персональные ключи аттестации устройств.
//
// Зачем: общий секрет во всех клиентах защищает ровно до первого, кто откроет
// APK или js-бандл. Персональный ключ живёт только на сервере и на конкретном
// устройстве, попадает туда по уже авторизованному каналу и никогда не
// оказывается в сборке. Кража ключа компрометирует одно устройство, а не всех
// сразу, и лечится отзывом этого устройства.

// newProofKey возвращает 32 случайных байта в hex.
func newProofKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// EnsureProofKey возвращает ключ аттестации устройства, создавая его при первом
// обращении. Второй результат — было ли устройство найдено вообще: несуществующему
// устройству ключ не выдаётся, иначе кто угодно получал бы валидный ключ, назвав
// произвольный uid.
//
// Отдельной проверки «не отозвано ли» здесь нет намеренно: RemoveDevice удаляет
// устройство из Devices и перестраивает индексы, так что присутствие в списке и
// означает «активно». Повторная привязка заводит запись заново, без ProofKey, —
// то есть отзыв честно убивает старый ключ, а не оставляет его работать.
func (s *Store) EnsureProofKey(token, uid string) (string, bool) {
	if token == "" || uid == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return "", false
	}
	for i, d := range s.tokens[idx].Devices {
		if d.UID != uid {
			continue
		}
		if d.ProofKey != "" {
			return d.ProofKey, true
		}
		key, err := newProofKey()
		if err != nil {
			return "", false
		}
		s.tokens[idx].Devices[i].ProofKey = key
		s.markDirty()
		return key, true
	}
	return "", false
}

// ProofKeyOf возвращает ключ устройства по его uid для ПРОВЕРКИ подписи.
// Ключа не создаёт: проверяющая сторона не должна порождать доверие.
// Отозванное устройство подписи не проходит.
func (s *Store) ProofKeyOf(uid string) (string, bool) {
	if uid == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byUID[uid]
	if !ok || idx < 0 || idx >= len(s.tokens) {
		return "", false
	}
	for _, d := range s.tokens[idx].Devices {
		if d.UID == uid {
			if d.ProofKey == "" {
				return "", false
			}
			return d.ProofKey, true
		}
	}
	return "", false
}

// SetDevicePubKey сохраняет публичный ключ, созданный в AndroidKeyStore
// устройства. Возвращает false, если устройство не найдено.
//
// Ключ принимается ОДИН РАЗ и потом не меняется: иначе укравший токен просто
// перезаписал бы чужой публичный ключ своим и получил бы ровно то же, что
// имеет сейчас с копией proof_key. Смена ключа — это новая привязка устройства,
// то есть сначала отвязка через бота.
func (s *Store) SetDevicePubKey(token, uid, pub string, hardware bool) (bool, error) {
	if token == "" || uid == "" || pub == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return false, nil
	}
	for i, d := range s.tokens[idx].Devices {
		if d.UID != uid {
			continue
		}
		if d.PubKey != "" {
			if d.PubKey == pub {
				return true, nil // тот же ключ — идемпотентно
			}
			return false, ErrPubKeyAlreadySet
		}
		s.tokens[idx].Devices[i].PubKey = pub
		s.tokens[idx].Devices[i].KeyHardware = hardware
		s.markDirty()
		return true, nil
	}
	return false, nil
}

// DevicePubKeyOf возвращает публичный ключ устройства для ПРОВЕРКИ подписи.
// Ничего не создаёт — проверяющая сторона не должна порождать доверие.
func (s *Store) DevicePubKeyOf(uid string) (pub string, hardware bool, ok bool) {
	if uid == "" {
		return "", false, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, found := s.byUID[uid]
	if !found || idx < 0 || idx >= len(s.tokens) {
		return "", false, false
	}
	for _, d := range s.tokens[idx].Devices {
		if d.UID == uid {
			if d.PubKey == "" {
				return "", false, false
			}
			return d.PubKey, d.KeyHardware, true
		}
	}
	return "", false, false
}
