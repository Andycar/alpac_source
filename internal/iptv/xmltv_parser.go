package iptv

import (
	"compress/gzip"
	"encoding/xml"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Streaming XMLTV parser
// ---------------------------------------------------------------------------

// ParseXMLTV parses an XMLTV document from r using a streaming xml.Decoder.
// Calls channelCb for each <channel> element and programCb for each <programme>.
// Supports very large XMLTV files (100MB+) without loading into memory.
func ParseXMLTV(r io.Reader, channelCb func(EPGChannel), programCb func(EPGProgram)) error {
	return ParseXMLTVFiltered(r, channelCb, nil, programCb)
}

// ParseXMLTVFiltered is like ParseXMLTV but can reject a programme using only
// attributes from the opening <programme> tag. Rejected programmes are skipped
// without decoding title/description/category text, which matters for multi-day
// XMLTV feeds.
func ParseXMLTVFiltered(r io.Reader, channelCb func(EPGChannel), programFilter func(EPGProgram) bool, programCb func(EPGProgram)) error {
	decoder := xml.NewDecoder(r)
	decoder.CharsetReader = charsetReader // handle non-UTF-8 (ISO-8859-1, etc.)

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "channel":
			ch, err := parseXMLTVChannel(decoder, se)
			if err != nil {
				log.Warn().Err(err).Msg("epg: skip malformed <channel>")
				continue
			}
			if channelCb != nil {
				channelCb(ch)
			}

		case "programme":
			prog := parseXMLTVProgrammeAttrs(se)
			if programFilter != nil && !programFilter(prog) {
				if err := decoder.Skip(); err != nil {
					log.Warn().Err(err).Msg("epg: skip malformed <programme>")
				}
				continue
			}
			prog, err := parseXMLTVProgrammeBody(decoder, se, prog)
			if err != nil {
				log.Warn().Err(err).Msg("epg: skip malformed <programme>")
				continue
			}
			if programCb != nil {
				programCb(prog)
			}
		}
	}

	return nil
}

// ParseXMLTVGzip wraps ParseXMLTV with gzip decompression.
func ParseXMLTVGzip(r io.Reader, channelCb func(EPGChannel), programCb func(EPGProgram)) error {
	return ParseXMLTVGzipFiltered(r, channelCb, nil, programCb)
}

// ParseXMLTVGzipFiltered wraps ParseXMLTVFiltered with gzip decompression.
func ParseXMLTVGzipFiltered(r io.Reader, channelCb func(EPGChannel), programFilter func(EPGProgram) bool, programCb func(EPGProgram)) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gr.Close()
	return ParseXMLTVFiltered(gr, channelCb, programFilter, programCb)
}

// ---------------------------------------------------------------------------
//  Internal: parse <channel>
// ---------------------------------------------------------------------------

func parseXMLTVChannel(decoder *xml.Decoder, se xml.StartElement) (EPGChannel, error) {
	ch := EPGChannel{}
	for _, attr := range se.Attr {
		if attr.Name.Local == "id" {
			ch.ID = attr.Value
		}
	}

	// Walk children until </channel>.
	for {
		tok, err := decoder.Token()
		if err != nil {
			return ch, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "display-name":
				if name, err := readElementText(decoder, t, epgMaxNameBytes); err == nil {
					if ch.Name == "" {
						ch.Name = name
					}
					if name != "" && len(ch.AltNames) < epgMaxAltNames {
						ch.AltNames = append(ch.AltNames, name)
					}
				}
			case "icon":
				for _, attr := range t.Attr {
					if attr.Name.Local == "src" && ch.Icon == "" {
						ch.Icon = attr.Value
					}
				}
				decoder.Skip()
			default:
				decoder.Skip()
			}
		case xml.EndElement:
			if t.Name.Local == "channel" {
				return ch, nil
			}
		}
	}
}

// ---------------------------------------------------------------------------
//  Internal: parse <programme>
// ---------------------------------------------------------------------------

func parseXMLTVProgramme(decoder *xml.Decoder, se xml.StartElement) (EPGProgram, error) {
	prog := parseXMLTVProgrammeAttrs(se)
	return parseXMLTVProgrammeBody(decoder, se, prog)
}

func parseXMLTVProgrammeAttrs(se xml.StartElement) EPGProgram {
	prog := EPGProgram{}
	for _, attr := range se.Attr {
		switch attr.Name.Local {
		case "start":
			prog.Start = parseXMLTVTime(attr.Value)
		case "stop":
			prog.Stop = parseXMLTVTime(attr.Value)
		case "channel":
			prog.ChannelID = attr.Value
		}
	}
	return prog
}

func parseXMLTVProgrammeBody(decoder *xml.Decoder, se xml.StartElement, prog EPGProgram) (EPGProgram, error) {
	// Walk children until </programme>.
	for {
		tok, err := decoder.Token()
		if err != nil {
			return prog, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "title":
				title, err := readElementText(decoder, t, epgMaxTitleBytes)
				if err == nil && prog.Title == "" {
					prog.Title = title
				}
			case "desc":
				desc, err := readElementText(decoder, t, epgMaxDescriptionBytes)
				if err == nil && prog.Description == "" {
					prog.Description = desc
				}
			case "category":
				cat, err := readElementText(decoder, t, epgMaxCategoryBytes)
				if err == nil && prog.Category == "" {
					prog.Category = cat
				}
			case "icon":
				for _, attr := range t.Attr {
					if attr.Name.Local == "src" && prog.Icon == "" {
						prog.Icon = attr.Value
					}
				}
				decoder.Skip()
			default:
				decoder.Skip()
			}
		case xml.EndElement:
			if t.Name.Local == "programme" {
				return prog, nil
			}
		}
	}
}

const (
	epgMaxTitleBytes = 512
	// Description is the heaviest per-programme field (avg ~400-555B) and the ALPAC client
	// NEVER renders it (verified: no .svelte reads program.desc) — it only fed the server-side
	// EPG Search. Real prod heap profile: programme strings = 410-709MB, desc ~88% of that.
	// Cap to a short teaser: keeps Search working on the lead text, cuts the dominant cost ~3x.
	epgMaxDescriptionBytes = 160
	epgMaxCategoryBytes    = 128
	epgMaxNameBytes        = 256
	epgMaxAltNames         = 8
)

func readElementText(decoder *xml.Decoder, se xml.StartElement, maxBytes int) (string, error) {
	var b strings.Builder
	for {
		tok, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			writeLimitedUTF8(&b, t, maxBytes)
		case xml.StartElement:
			if err := decoder.Skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			if t.Name.Local == se.Name.Local {
				return strings.TrimSpace(b.String()), nil
			}
		}
	}
}

func writeLimitedUTF8(b *strings.Builder, data []byte, maxBytes int) {
	if len(data) == 0 || (maxBytes > 0 && b.Len() >= maxBytes) {
		return
	}
	if maxBytes > 0 && b.Len()+len(data) > maxBytes {
		data = data[:maxBytes-b.Len()]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	_, _ = b.Write(data)
}

// ---------------------------------------------------------------------------
//  XMLTV time format: "20060102150405 -0700" or "20060102150405"
// ---------------------------------------------------------------------------

var xmltvTimeFormats = []string{
	"20060102150405 -0700",
	"20060102150405 +0700",
	"20060102150405",
	"200601021504 -0700",
	"200601021504",
}

func parseXMLTVTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range xmltvTimeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// charsetReader handles non-UTF-8 encodings in XML.
// For ISO-8859-1 / latin1 we can just pass through since bytes < 256
// are identical code points in Unicode.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	lower := strings.ToLower(charset)
	switch lower {
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1":
		return input, nil // ASCII superset — pass through
	}
	// For unknown charsets, try passthrough and hope for the best.
	return input, nil
}
