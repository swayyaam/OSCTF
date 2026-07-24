// Package pagination normalizes page/per_page query params into limit/offset.
package pagination

// Params is a normalized pagination request.
type Params struct {
	Page    int
	PerPage int
}

// maxPage bounds the page number so limit/offset stay within int32.
const maxPage = 1_000_000

// Normalize clamps page (1..maxPage) and per_page (1..200, default 50).
func Normalize(page, perPage *int) Params {
	p := Params{Page: 1, PerPage: 50}
	if page != nil && *page >= 1 {
		p.Page = *page
		if p.Page > maxPage {
			p.Page = maxPage
		}
	}
	if perPage != nil {
		switch {
		case *perPage < 1:
			p.PerPage = 1
		case *perPage > 200:
			p.PerPage = 200
		default:
			p.PerPage = *perPage
		}
	}
	return p
}

// Limit returns the SQL LIMIT. PerPage is clamped to 1..200, so int32 is safe.
//
//nolint:gosec // G115: PerPage is clamped to [1,200] in Normalize.
func (p Params) Limit() int32 { return int32(p.PerPage) }

// Offset returns the SQL OFFSET. Page<=maxPage and PerPage<=200 keep this in int32.
//
//nolint:gosec // G115: Page and PerPage are clamped in Normalize.
func (p Params) Offset() int32 { return int32((p.Page - 1) * p.PerPage) }
