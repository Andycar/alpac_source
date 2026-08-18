package proxyapi

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"

	"github.com/rs/zerolog/log"
)

// stripFMP4DRM removes DRM signaling from fMP4 init segments.
//
// Kinescope uses SAMPLE-AES (CBCS scheme) with fMP4. The container declares
// encryption via encv/enca sample entries with nested sinf boxes.
//
// For init segments (moov): physically removes sinf boxes from encv/enca
// entries, renames encv→avc1 / enca→mp4a, and adjusts all parent box sizes.
// A compensating "free" box is inserted inside moov so the total init
// segment size doesn't change (preserving BYTERANGE playlist offsets).
//
// For media segments (moof+mdat): replaces senc/saiz/saio with free boxes.
//
// Modifies data IN-PLACE; returned slice is always the same length.
func stripFMP4DRM(data []byte) []byte {
	stripFMP4DRMInPlace(data)
	return data
}

// stripFMP4DRMInPlace handles both init segments (moov) and media segments (moof+mdat).
// For init: removes sinf from sample entries, renames encv/enca, adjusts parent sizes,
// inserts compensating free box inside moov.
// For media: replaces senc/saiz/saio with free boxes.
func stripFMP4DRMInPlace(data []byte) {
	// Process all top-level boxes.
	offset := 0
	for offset+8 <= len(data) {
		boxSize := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])
		if boxSize < 8 {
			break
		}
		if offset+boxSize > len(data) {
			break
		}

		switch boxType {
		case "moov":
			stripMoovDRM(data[offset : offset+boxSize])
		case "moof":
			stripMoofDRM(data[offset : offset+boxSize])
		}

		offset += boxSize
	}
}

// stripMoovDRM processes a moov box: finds the path
// moov → trak → mdia → minf → stbl → stsd → encv/enca,
// removes sinf boxes, and adjusts all parent box sizes.
// A compensating free box is written over the freed space at the end of moov.
func stripMoovDRM(moov []byte) {
	moovSize := int(binary.BigEndian.Uint32(moov[0:4]))
	if moovSize < 8 || moovSize > len(moov) {
		return
	}

	// Build a chain of offsets: moov → trak → mdia → minf → stbl → stsd.
	// Each entry is the absolute offset within moov[] of that box.
	type boxRef struct {
		off  int // offset of box start within moov[]
		size int // box size
	}

	// Find trak inside moov.
	findChild := func(parent []byte, parentOff int, name string) (boxRef, bool) {
		pos := 8 // skip box header
		for pos+8 <= len(parent) {
			sz := int(binary.BigEndian.Uint32(parent[pos : pos+4]))
			bt := string(parent[pos+4 : pos+8])
			if sz < 8 || pos+sz > len(parent) {
				break
			}
			if bt == name {
				return boxRef{off: parentOff + pos, size: sz}, true
			}
			pos += sz
		}
		return boxRef{}, false
	}

	findNthChild := func(parent []byte, parentOff int, name string, index int) (boxRef, bool) {
		pos := 8 // skip box header
		found := 0
		for pos+8 <= len(parent) {
			sz := int(binary.BigEndian.Uint32(parent[pos : pos+4]))
			bt := string(parent[pos+4 : pos+8])
			if sz < 8 || pos+sz > len(parent) {
				break
			}
			if bt == name {
				if found == index {
					return boxRef{off: parentOff + pos, size: sz}, true
				}
				found++
			}
			pos += sz
		}
		return boxRef{}, false
	}

	processTrack := func(trak boxRef) {
		trakData := moov[trak.off : trak.off+trak.size]

		mdia, ok := findChild(trakData, trak.off, "mdia")
		if !ok {
			return
		}
		mdiaData := moov[mdia.off : mdia.off+mdia.size]

		minf, ok := findChild(mdiaData, mdia.off, "minf")
		if !ok {
			return
		}
		minfData := moov[minf.off : minf.off+minf.size]

		stbl, ok := findChild(minfData, minf.off, "stbl")
		if !ok {
			return
		}
		stblData := moov[stbl.off : stbl.off+stbl.size]

		stsd, ok := findChild(stblData, stbl.off, "stsd")
		if !ok {
			return
		}

		// Process encv/enca entries inside stsd.
		// stsd layout: header(8) + version/flags(4) + entry_count(4) + entries
		stsdPayloadOff := stsd.off + 16
		stsdPayloadLen := stsd.size - 16
		if stsdPayloadLen <= 0 {
			return
		}
		stsdPayload := moov[stsdPayloadOff : stsdPayloadOff+stsdPayloadLen]

		removed := processSTSDEntriesInPlace(stsdPayload)
		if removed <= 0 {
			return
		}

		// Shrink parent boxes (stsd through trak) by 'removed' bytes.
		// moov size stays the same — compensated by a free box at the end.
		parents := []int{stsd.off, stbl.off, minf.off, mdia.off, trak.off}
		for _, off := range parents {
			oldSz := int(binary.BigEndian.Uint32(moov[off : off+4]))
			binary.BigEndian.PutUint32(moov[off:off+4], uint32(oldSz-removed))
		}

		// Shift data after stsd entry content to close the gap.
		gapStart := stsdPayloadOff + stsdPayloadLen - removed
		gapEnd := stsdPayloadOff + stsdPayloadLen
		if gapEnd < moovSize {
			copy(moov[gapStart:], moov[gapEnd:moovSize])
		}

		// Write a compensating free box in the freed space at the end of moov.
		// This keeps total moov size (and therefore init segment size) unchanged,
		// so BYTERANGE offsets in the HLS playlist remain valid.
		freeOff := moovSize - removed
		if removed >= 8 {
			binary.BigEndian.PutUint32(moov[freeOff:freeOff+4], uint32(removed))
			copy(moov[freeOff+4:freeOff+8], "free")
			for i := freeOff + 8; i < moovSize; i++ {
				moov[i] = 0
			}
		}
	}

	// Process all tracks, not only the first one.
	for i := 0; ; i++ {
		trak, ok := findNthChild(moov, 0, "trak", i)
		if !ok {
			break
		}
		processTrack(trak)
	}
}

// stripMoofDRM replaces senc/saiz/saio boxes inside moof/traf with free boxes.
func stripMoofDRM(moof []byte) {
	pos := 8 // skip moof header
	for pos+8 <= len(moof) {
		sz := int(binary.BigEndian.Uint32(moof[pos : pos+4]))
		bt := string(moof[pos+4 : pos+8])
		if sz < 8 || pos+sz > len(moof) {
			break
		}
		if bt == "traf" {
			neutralizeEncryptionBoxes(moof[pos+8 : pos+sz])
		}
		pos += sz
	}
}

// neutralizeEncryptionBoxes replaces saiz, saio, and senc boxes with free
// boxes of the same size. These boxes declare per-sample encryption metadata
// in moof/traf. Without neutralizing them, hls.js tries to decrypt and fails.
func neutralizeEncryptionBoxes(data []byte) {
	offset := 0
	for offset+8 <= len(data) {
		boxSize := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])

		if boxSize < 8 {
			if boxSize == 0 {
				return
			}
			break
		}
		if offset+boxSize > len(data) {
			break
		}

		if boxType == "senc" || boxType == "saiz" || boxType == "saio" {
			copy(data[offset+4:offset+8], "free")
			for i := offset + 8; i < offset+boxSize; i++ {
				data[i] = 0
			}
		}

		offset += boxSize
	}
}

// processSTSDEntriesInPlace patches encv→avc1, enca→mp4a in-place
// and physically removes sinf sub-boxes (shifting bytes left).
// Returns the total number of bytes removed (sum of all sinf box sizes).
// After return, the first (len(data) - removed) bytes are valid.
func processSTSDEntriesInPlace(data []byte) int {
	totalRemoved := 0
	offset := 0
	validLen := len(data)

	for offset+8 <= validLen {
		boxSize := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])

		if boxSize < 8 {
			break
		}
		if offset+boxSize > validLen {
			break
		}

		if boxType == "encv" || boxType == "enca" {
			isVideo := boxType == "encv"
			// Rename in-place.
			if isVideo {
				copy(data[offset+4:offset+8], "avc1")
			} else {
				copy(data[offset+4:offset+8], "mp4a")
			}

			// Fixed part of sample entry: video=86, audio=36.
			fixedLen := 36
			if isVideo {
				fixedLen = 86
			}

			// Find and remove sinf inside this sample entry's sub-boxes.
			subStart := offset + fixedLen
			subEnd := offset + boxSize
			if subStart < subEnd {
				removed := removeSinfBoxes(data, subStart, subEnd)
				if removed > 0 {
					// Shrink this sample entry box.
					newBoxSize := boxSize - removed
					binary.BigEndian.PutUint32(data[offset:offset+4], uint32(newBoxSize))

					// Shift everything after this entry left.
					afterEntry := offset + boxSize
					if afterEntry < validLen {
						copy(data[afterEntry-removed:], data[afterEntry:validLen])
					}
					validLen -= removed
					totalRemoved += removed

					// Advance by new (shrunk) box size.
					offset += newBoxSize
					continue
				}
			}
		}

		offset += boxSize
	}
	return totalRemoved
}

// removeSinfBoxes removes all sinf boxes from data[start:end] in-place.
// Returns total bytes removed. After removal, data is shifted left;
// the caller must handle adjusting parent sizes and subsequent data.
func removeSinfBoxes(data []byte, start, end int) int {
	totalRemoved := 0
	offset := start
	validEnd := end

	for offset+8 <= validEnd {
		boxSize := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])

		if boxSize < 8 || offset+boxSize > validEnd {
			break
		}

		if boxType == "sinf" {
			// Shift remaining data left to remove sinf.
			afterSinf := offset + boxSize
			if afterSinf < validEnd {
				copy(data[offset:], data[afterSinf:validEnd])
			}
			validEnd -= boxSize
			totalRemoved += boxSize
			// Don't advance — next box is now at 'offset'.
			continue
		}

		offset += boxSize
	}
	return totalRemoved
}

// -----------------------------------------------------------------------
// CBCS decryption for Kinescope fMP4 segments
// -----------------------------------------------------------------------

// trunSample holds per-sample data from a trun box.
type trunSample struct {
	size uint32
}

// sencSubsample holds clear/protected byte counts from senc.
type sencSubsample struct {
	bytesOfClearData     uint16
	bytesOfProtectedData uint32
}

// sencEntry holds per-sample encryption info from senc.
type sencEntry struct {
	subsamples []sencSubsample
}

// decryptFMP4CBCS decrypts a CBCS-encrypted fMP4 media segment in-place.
// It parses moof (trun for sample sizes, senc for subsample patterns),
// then decrypts each sample's protected ranges in mdat using AES-128-CBC
// with pattern (crypt=1, skip=9). After decryption, DRM metadata is stripped.
func decryptFMP4CBCS(data, key, iv []byte) {
	if len(key) != 16 || len(iv) != 16 {
		log.Debug().Msg("cbcs: invalid key/iv length")
		stripFMP4DRMInPlace(data)
		return
	}

	// Find moof and mdat offsets at the top level.
	moofOff, moofSize := -1, 0
	mdatOff, mdatSize := -1, 0
	pos := 0
	for pos+8 <= len(data) {
		sz := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		bt := string(data[pos+4 : pos+8])
		if sz < 8 {
			break
		}
		if pos+sz > len(data) {
			break
		}
		if bt == "moof" {
			moofOff, moofSize = pos, sz
		} else if bt == "mdat" {
			mdatOff, mdatSize = pos, sz
		}
		pos += sz
	}

	if moofOff < 0 || mdatOff < 0 {
		log.Debug().Msg("cbcs: no moof/mdat found, strip only")
		stripFMP4DRMInPlace(data)
		return
	}

	// Parse traf inside moof to get trun and senc.
	moofData := data[moofOff : moofOff+moofSize]
	dataOffset, samples := parseMoofForDecrypt(moofData)
	if dataOffset < 0 || len(samples.trunSamples) == 0 {
		log.Debug().Int("dataOffset", dataOffset).Int("samples", len(samples.trunSamples)).
			Msg("cbcs: no trun samples, strip only")
		stripFMP4DRMInPlace(data)
		return
	}

	// data_offset is relative to the start of moof.
	mdatPayloadStart := moofOff + dataOffset
	if mdatPayloadStart < mdatOff+8 || mdatPayloadStart >= mdatOff+mdatSize {
		// Fallback: mdat payload starts right after mdat header.
		mdatPayloadStart = mdatOff + 8
	}

	log.Debug().
		Int("samples", len(samples.trunSamples)).
		Int("sencEntries", len(samples.sencEntries)).
		Msg("cbcs: decrypting segment")

	// Create AES cipher block.
	block, err := aes.NewCipher(key)
	if err != nil {
		log.Debug().Err(err).Msg("cbcs: aes.NewCipher failed")
		stripFMP4DRMInPlace(data)
		return
	}

	// Decrypt each sample.
	sampleOffset := mdatPayloadStart
	for i, ts := range samples.trunSamples {
		sampleEnd := sampleOffset + int(ts.size)
		if sampleEnd > len(data) {
			break
		}

		sampleData := data[sampleOffset:sampleEnd]

		if i < len(samples.sencEntries) && len(samples.sencEntries[i].subsamples) > 0 {
			// Has subsample patterns — video track.
			// CBCS pattern: crypt=1, skip=9 (from tenc).
			// For each subsample: skip clear bytes, then decrypt protected
			// range using AES-128-CBC with pattern (1 block encrypted, 9 skipped).
			decryptSampleCBCS(sampleData, block, iv, samples.sencEntries[i].subsamples)
		} else {
			// No subsample info — audio track.
			// Kinescope audio tenc: crypt=0, skip=0 → full AES-128-CBC.
			// IV resets to constant IV per sample.
			decryptFullCBC(sampleData, block, iv)
		}

		sampleOffset = sampleEnd
	}

	// Strip DRM metadata (encv→avc1, sinf→free, senc→free, etc.).
	stripFMP4DRMInPlace(data)
}

// moofDecryptInfo holds parsed trun + senc data from a moof box.
type moofDecryptInfo struct {
	trunSamples []trunSample
	sencEntries []sencEntry
}

// parseMoofForDecrypt extracts trun and senc data from a moof box.
// Returns data_offset (relative to moof start) and parsed info.
func parseMoofForDecrypt(moofData []byte) (int, moofDecryptInfo) {
	var info moofDecryptInfo
	dataOffset := -1

	// Walk moof children to find traf.
	pos := 8 // skip moof header
	for pos+8 <= len(moofData) {
		sz := int(binary.BigEndian.Uint32(moofData[pos : pos+4]))
		bt := string(moofData[pos+4 : pos+8])
		if sz < 8 || pos+sz > len(moofData) {
			break
		}
		if bt == "traf" {
			do, ti := parseTraf(moofData[pos+8 : pos+sz])
			if do >= 0 {
				dataOffset = pos + 8 + do // traf-relative offset → moof-relative
			}
			// Actually trun data_offset is relative to moof start.
			// parseTrun returns the raw data_offset from the box.
			if do >= 0 {
				dataOffset = do
			}
			info.trunSamples = ti.trunSamples
			info.sencEntries = ti.sencEntries
			break // process first traf only
		}
		pos += sz
	}
	return dataOffset, info
}

// parseTraf walks traf children to extract trun and senc.
func parseTraf(trafData []byte) (int, moofDecryptInfo) {
	var info moofDecryptInfo
	dataOffset := -1

	pos := 0
	for pos+8 <= len(trafData) {
		sz := int(binary.BigEndian.Uint32(trafData[pos : pos+4]))
		bt := string(trafData[pos+4 : pos+8])
		if sz < 8 || pos+sz > len(trafData) {
			break
		}

		switch bt {
		case "trun":
			do, samples := parseTrun(trafData[pos : pos+sz])
			if len(samples) > 0 {
				dataOffset = do
				info.trunSamples = samples
			}
		case "senc":
			entries := parseSenc(trafData[pos : pos+sz])
			info.sencEntries = entries
		}

		pos += sz
	}
	return dataOffset, info
}

// parseTrun parses a trun box and returns data_offset and per-sample sizes.
// trun format: FullBox(version, flags) + sample_count + [data_offset] + [first_sample_flags]
// Per sample (depending on flags): [duration] [size] [flags] [composition_time_offset]
func parseTrun(boxData []byte) (int, []trunSample) {
	if len(boxData) < 16 { // header(8) + version/flags(4) + sample_count(4)
		return -1, nil
	}

	vf := binary.BigEndian.Uint32(boxData[8:12])
	flags := vf & 0x00FFFFFF
	sampleCount := int(binary.BigEndian.Uint32(boxData[12:16]))
	if sampleCount <= 0 || sampleCount > 100000 {
		return -1, nil
	}

	offset := 16
	dataOffset := -1

	// data-offset-present (0x1)
	if flags&0x1 != 0 {
		if offset+4 > len(boxData) {
			return -1, nil
		}
		dataOffset = int(int32(binary.BigEndian.Uint32(boxData[offset : offset+4])))
		offset += 4
	}

	// first-sample-flags-present (0x4)
	if flags&0x4 != 0 {
		offset += 4
	}

	// Calculate per-sample record size.
	recordSize := 0
	hasDuration := flags&0x100 != 0
	hasSize := flags&0x200 != 0
	hasFlags := flags&0x400 != 0
	hasCompOffset := flags&0x800 != 0
	if hasDuration {
		recordSize += 4
	}
	if hasSize {
		recordSize += 4
	}
	if hasFlags {
		recordSize += 4
	}
	if hasCompOffset {
		recordSize += 4
	}

	if recordSize == 0 || !hasSize {
		return dataOffset, nil
	}

	samples := make([]trunSample, 0, sampleCount)
	for range sampleCount {
		if offset+recordSize > len(boxData) {
			break
		}

		rec := boxData[offset : offset+recordSize]
		recOff := 0

		if hasDuration {
			recOff += 4
		}
		var size uint32
		if hasSize {
			size = binary.BigEndian.Uint32(rec[recOff : recOff+4])
		}

		samples = append(samples, trunSample{size: size})
		offset += recordSize
	}
	return dataOffset, samples
}

// parseSenc parses a senc box and returns per-sample subsample patterns.
// senc: FullBox(version, flags) + sample_count + per-sample entries.
// If flags & 0x2 (UseSubSampleEncryption): each sample has subsample_count + pairs.
// With per_sample_iv_size=0 (constant IV), there are no per-sample IVs.
func parseSenc(boxData []byte) []sencEntry {
	if len(boxData) < 16 {
		return nil
	}

	vf := binary.BigEndian.Uint32(boxData[8:12])
	flags := vf & 0x00FFFFFF
	sampleCount := int(binary.BigEndian.Uint32(boxData[12:16]))
	if sampleCount <= 0 || sampleCount > 100000 {
		return nil
	}

	useSubSample := flags&0x2 != 0
	offset := 16

	entries := make([]sencEntry, 0, sampleCount)
	for range sampleCount {
		var entry sencEntry

		// Per-sample IV: with per_sample_iv_size=0 (Kinescope), no IV here.
		// We skip 0 bytes for IV.

		if useSubSample {
			if offset+2 > len(boxData) {
				break
			}
			subCount := int(binary.BigEndian.Uint16(boxData[offset : offset+2]))
			offset += 2

			entry.subsamples = make([]sencSubsample, 0, subCount)
			for range subCount {
				if offset+6 > len(boxData) {
					break
				}
				clearBytes := binary.BigEndian.Uint16(boxData[offset : offset+2])
				protectedBytes := binary.BigEndian.Uint32(boxData[offset+2 : offset+6])
				entry.subsamples = append(entry.subsamples, sencSubsample{
					bytesOfClearData:     clearBytes,
					bytesOfProtectedData: protectedBytes,
				})
				offset += 6
			}
		}

		entries = append(entries, entry)
	}
	return entries
}

// decryptSampleCBCS decrypts a single sample using CBCS with subsample patterns.
// For each subsample: skip clear bytes, then decrypt the protected range
// using AES-128-CBC with pattern (crypt=1, skip=9).
// The IV resets to constant IV at each subsample boundary.
func decryptSampleCBCS(sampleData []byte, block cipher.Block, iv []byte, subsamples []sencSubsample) {
	pos := 0
	for _, ss := range subsamples {
		// Skip clear bytes.
		pos += int(ss.bytesOfClearData)
		if pos >= len(sampleData) {
			break
		}

		// Decrypt protected range with CBCS pattern 1:9.
		protectedEnd := min(pos+int(ss.bytesOfProtectedData), len(sampleData))
		protectedData := sampleData[pos:protectedEnd]
		decryptCBCSPattern(protectedData, block, iv, 1, 9)
		pos = protectedEnd
	}
}

// decryptCBCSPattern decrypts data using AES-128-CBC with pattern encryption.
// cryptBlocks blocks are decrypted, then skipBlocks blocks are left clear.
// Per CBCS spec (ISO 23001-7): with constant IV (per_sample_iv_size=0),
// the IV resets to the constant IV for EACH crypt block group — there is
// NO IV chaining between pattern groups.
func decryptCBCSPattern(data []byte, block cipher.Block, iv []byte, cryptBlocks, skipBlocks int) {
	cryptSize := cryptBlocks * 16 // typically 16 bytes
	skipSize := skipBlocks * 16   // typically 144 bytes

	// IV chains across pattern groups: the ciphertext of the current crypt block
	// becomes the IV for the next crypt block (standard CBC chaining through
	// skip blocks). First group uses the constant IV from tenc/m3u8.
	currentIV := make([]byte, 16)
	copy(currentIV, iv)

	pos := 0
	for pos+cryptSize <= len(data) {
		// Save ciphertext before decryption — it becomes the IV for the next group.
		copy(currentIV, data[pos:pos+cryptSize])

		decrypter := cipher.NewCBCDecrypter(block, iv)
		decrypter.CryptBlocks(data[pos:pos+cryptSize], data[pos:pos+cryptSize])
		pos += cryptSize

		// Update IV for next group.
		iv = make([]byte, 16)
		copy(iv, currentIV)

		// Skip skipBlocks * 16 bytes (leave clear).
		skip := skipSize
		if pos+skip > len(data) {
			skip = len(data) - pos
		}
		pos += skip
	}
	// Remaining bytes < cryptSize are left unencrypted.
}

// decryptFullCBC decrypts data using plain AES-128-CBC (no pattern).
// Used for audio tracks where tenc crypt_byte_block=0, skip_byte_block=0.
// Only full 16-byte blocks are decrypted; trailing bytes < 16 are left as-is.
func decryptFullCBC(data []byte, block cipher.Block, iv []byte) {
	n := len(data) &^ 15 // round down to 16-byte boundary
	if n == 0 {
		return
	}
	decrypter := cipher.NewCBCDecrypter(block, iv)
	decrypter.CryptBlocks(data[:n], data[:n])
}

// hasFMP4DRMBoxes does a quick check to see if the data likely contains
// encrypted sample entries (encv/enca). This is a fast pre-filter to
// avoid full parsing on every segment — only init segments have stsd boxes.
func hasFMP4DRMBoxes(data []byte) bool {
	// Look for "encv" or "enca" box type markers.
	for i := 4; i+4 <= len(data); i++ {
		if string(data[i:i+4]) == "encv" || string(data[i:i+4]) == "enca" {
			// Verify it looks like a valid box size.
			if i >= 4 {
				size := int(binary.BigEndian.Uint32(data[i-4 : i]))
				if size >= 8 && i-4+size <= len(data)+1000 { // allow some tolerance
					return true
				}
			}
		}
	}
	return false
}
