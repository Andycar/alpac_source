//go:build windows

package transcodesvc

// Сторож «ffmpeg вне учёта» живёт на /proc и сигналах — под Windows его нет.
func (svc *TranscodingService) reapOrphanProcs() (killed, reaped int) { return 0, 0 }
