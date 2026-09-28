package console

// The level registries as data, for the docs site. cmd/docsgen renders the
// "what each level shows" matrix from these rows, so the published matrix is
// the gates themselves rather than a transcription of them -- a screen, a
// Settings section, or an in-page surface that changes level changes the docs
// on the next `make docs`, and docs-check fails the build if nobody ran it.

// LevelMatrixRow is one line of the docs matrix: something the console shows,
// and the first level that shows it.
type LevelMatrixRow struct {
	// Group is "Screens", "Settings sections", or "Within screens".
	Group string
	// Name is what the owner sees it called.
	Name string
	// Note qualifies the row ("research feature", "no sidebar entry").
	Note string
	// Min is the first level (a config.ConsoleLevels value) that shows it.
	Min string
}

// Level matrix groups, in the order the docs render them.
const (
	MatrixScreens  = "Screens"
	MatrixSections = "Settings sections"
	MatrixSurfaces = "Within screens"
)

// LevelMatrix returns every row, grouped and in registry order.
func LevelMatrix() []LevelMatrixRow {
	out := make([]LevelMatrixRow, 0, len(screens)+len(settingsSections)+len(surfaces))
	for _, sc := range screens {
		row := LevelMatrixRow{Group: MatrixScreens, Name: sc.Label, Min: sc.Min.String()}
		switch {
		case sc.Feature != "":
			row.Note = "while the " + string(sc.Feature) + " feature is on"
		case !sc.NavRow:
			row.Note = "no sidebar entry: the palette and links reach it"
		}
		out = append(out, row)
	}
	for _, sec := range settingsSections {
		out = append(out, LevelMatrixRow{Group: MatrixSections, Name: sec.Label, Note: sec.Blurb, Min: sec.Min.String()})
	}
	for _, sf := range surfaces {
		out = append(out, LevelMatrixRow{Group: MatrixSurfaces, Name: sf.Where + ": " + sf.Label, Min: sf.Min.String()})
	}
	return out
}
