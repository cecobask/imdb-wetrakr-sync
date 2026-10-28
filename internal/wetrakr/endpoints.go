package wetrakr

import (
	"context"
	"net/http"
	"net/url"
)

// Account returns the signed-in user.
func (c *Client) Account(ctx context.Context) (*Account, error) {
	var a Account
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/account/settings", auth: true}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Tracking returns every title in the given tracking status, across movies, shows, seasons and episodes.
func (c *Client) Tracking(ctx context.Context, status string) ([]Row, error) {
	rows, err := c.compactRows(ctx, "/sync/tracking/"+url.PathEscape(status)+"/all", nil)
	for i := range rows {
		if rows[i].Status == "" {
			rows[i].Status = status
		}
	}
	return rows, err
}

// AddTracking sets the tracking status of the items.
func (c *Client) AddTracking(ctx context.Context, items Items) (WriteResult, error) {
	return c.write(ctx, "/sync/tracking", items)
}

// RemoveTracking removes the items from the tracking status given on each item.
func (c *Client) RemoveTracking(ctx context.Context, items Items) (WriteResult, error) {
	return c.write(ctx, "/sync/tracking/remove", items)
}

// Ratings returns every rating of the user.
func (c *Client) Ratings(ctx context.Context) ([]Row, error) {
	return c.compactRows(ctx, "/sync/ratings/all", nil)
}

// AddRatings adds or replaces ratings.
func (c *Client) AddRatings(ctx context.Context, items Items) (WriteResult, error) {
	return c.write(ctx, "/sync/ratings", items)
}

// RemoveRatings removes ratings.
func (c *Client) RemoveRatings(ctx context.Context, items Items) (WriteResult, error) {
	return c.write(ctx, "/sync/ratings/remove", items)
}

// Lists returns the lists the user owns.
func (c *Client) Lists(ctx context.Context) ([]List, error) {
	return pagedGet[List](ctx, c, "/sync/lists", 100)
}

// CreateList creates a list.
func (c *Client) CreateList(ctx context.Context, in ListInput) (*List, error) {
	var l List
	if _, err := c.do(ctx, request{method: http.MethodPost, path: "/sync/lists", body: in, auth: true}, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// UpdateList changes the fields of the list that are set in the input.
func (c *Client) UpdateList(ctx context.Context, id int64, in ListInput) error {
	_, err := c.do(ctx, request{method: http.MethodPut, path: "/sync/lists/" + itoa(id), body: in, auth: true}, nil)
	return err
}

// listItem is an entry of a list. The media can be inline (type, id, ids) or nested under its type.
type listItem struct {
	Row
	Movie   *Row `json:"movie,omitempty"`
	Show    *Row `json:"show,omitempty"`
	Season  *Row `json:"season,omitempty"`
	Episode *Row `json:"episode,omitempty"`
	Person  *Row `json:"person,omitempty"`
}

func (li listItem) row() Row {
	nested := []struct {
		row *Row
		typ string
	}{{li.Movie, TypeMovie}, {li.Show, TypeShow}, {li.Season, TypeSeason}, {li.Episode, TypeEpisode}, {li.Person, TypePerson}}
	for _, n := range nested {
		if n.row != nil {
			r := *n.row
			if r.Type == "" {
				r.Type = n.typ
			}
			return r
		}
	}
	return li.Row
}

// ListItems returns the items of a list.
func (c *Client) ListItems(ctx context.Context, id int64) ([]Row, error) {
	items, err := pagedGet[listItem](ctx, c, "/sync/lists/"+itoa(id)+"/items", 100)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(items))
	for _, li := range items {
		rows = append(rows, li.row())
	}
	return rows, nil
}

// AddListItems adds items to a list.
func (c *Client) AddListItems(ctx context.Context, id int64, items Items) (WriteResult, error) {
	return c.write(ctx, "/sync/lists/"+itoa(id)+"/items", items)
}

// RemoveListItems removes items from a list.
func (c *Client) RemoveListItems(ctx context.Context, id int64, items Items) (WriteResult, error) {
	return c.write(ctx, "/sync/lists/"+itoa(id)+"/items/remove", items)
}

// LookupExternal resolves an external id, such as an IMDb tt… or nm… id, to the WeTrakr id.
// mediaType is optional and can be movie, show or person.
func (c *Client) LookupExternal(ctx context.Context, source, id, mediaType string) (*ExternalMatch, error) {
	var q url.Values
	if mediaType != "" {
		q = url.Values{"type": {mediaType}}
	}
	path := "/media/external/" + url.PathEscape(source) + "/" + url.PathEscape(id)
	var m ExternalMatch
	if _, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q, auth: true}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
