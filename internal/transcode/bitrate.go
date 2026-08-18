package transcode

// PickVideoBitrate returns a target encode bitrate (kbps) for the given output
// dimensions. Kept consistent between the ABR ladder planner and the HW encoder
// block. Carved out of the httpapi hwaccel file when this package was extracted.
func PickVideoBitrate(width, height int) int {
	px := width * height
	switch {
	case px <= 854*480:
		return 1500
	case px <= 1280*720:
		return 3000
	case px <= 1920*1080:
		return 6000
	case px <= 2560*1440:
		return 10000
	default: // 4K and above
		return 16000
	}
}
