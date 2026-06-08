package zoom

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
)

func decodeJSON(r io.Reader, out any) error {
	return json.NewDecoder(r).Decode(out)
}

// parseCSV parses the Zoom usage-report CSV into meeting rows, keyed off the
// header names (column order isn't assumed). Rows with an unparseable start
// time are skipped.
func parseCSV(r io.Reader) ([]meeting, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	header, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(strings.ToLower(h))] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	var out []meeting
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		st, ok := parseZoomTime(get(rec, "start time"))
		if !ok {
			continue
		}
		m := meeting{
			Topic:        get(rec, "topic"),
			Start:        st,
			DurationMin:  parseFloat(get(rec, "duration (minutes)")),
			Participants: int(parseFloat(get(rec, "participants"))),
			ParticipantM: parseFloat(get(rec, "total participant minutes")),
		}
		out = append(out, m)
	}
	return out, nil
}

// parseZoomTime parses Zoom's CSV timestamp, e.g. "05/04/2026 11:00:41 AM".
func parseZoomTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		"01/02/2006 03:04:05 PM",
		"01/02/2006 3:04:05 PM",
		"01/02/2006 15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseFloat(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
