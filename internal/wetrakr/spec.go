package wetrakr

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Tracking statuses. A title has at most one status at a time.
const (
	StatusPlanning = "planning"
	StatusWatching = "watching"
	StatusWaiting  = "waiting"
	StatusWatched  = "watched"
	StatusDropped  = "dropped"
	StatusHold     = "hold"
	StatusPaused   = "paused"
)

// AllStatuses is every tracking status a title can be in.
var AllStatuses = []string{StatusPlanning, StatusWatching, StatusWaiting, StatusWatched, StatusDropped, StatusHold, StatusPaused}

// Media types as used in the "type" field of responses.
const (
	TypeMovie   = "movie"
	TypeShow    = "show"
	TypeSeason  = "season"
	TypeEpisode = "episode"
	TypePerson  = "person"
)

// IDs holds the external ids of a media item. Only the IMDb id is needed for matching.
type IDs struct {
	IMDb string `json:"imdb,omitempty"`
}

// Row is a compact row returned by the tracking, ratings and list item endpoints.
type Row struct {
	Type      string  `json:"type"`
	ID        int64   `json:"id"`
	IDs       IDs     `json:"ids"`
	Title     string  `json:"title,omitempty"`
	Status    string  `json:"status,omitempty"`
	Rating    float64 `json:"rating,omitempty"`
	TrackedAt string  `json:"tracked_at,omitempty"`
	RatedAt   string  `json:"rated_at,omitempty"`
}

// Item is one entry in the body of a write request. Either ID (WeTrakr id) or IDs is set.
type Item struct {
	ID        int64  `json:"id,omitempty"`
	IDs       *IDs   `json:"ids,omitempty"`
	Status    string `json:"status,omitempty"`
	TrackedAt string `json:"tracked_at,omitempty"`
	// TrackedAtUnknown records a play without a date.
	TrackedAtUnknown bool     `json:"tracked_at_unknown,omitempty"`
	Rating           *float64 `json:"rating,omitempty"`
	RatedAt          string   `json:"rated_at,omitempty"`
}

// Items is the body of the write endpoints: the items are grouped by media type.
type Items struct {
	Movies       []Item `json:"movies,omitempty"`
	Shows        []Item `json:"shows,omitempty"`
	Seasons      []Item `json:"seasons,omitempty"`
	Episodes     []Item `json:"episodes,omitempty"`
	People       []Item `json:"people,omitempty"`
	AllowRewatch *bool  `json:"allow_rewatch,omitempty"`
}

// Add appends an item to the group that matches the media type.
func (i *Items) Add(mediaType string, item Item) {
	switch mediaType {
	case TypeMovie:
		i.Movies = append(i.Movies, item)
	case TypeShow:
		i.Shows = append(i.Shows, item)
	case TypeSeason:
		i.Seasons = append(i.Seasons, item)
	case TypeEpisode:
		i.Episodes = append(i.Episodes, item)
	case TypePerson:
		i.People = append(i.People, item)
	}
}

// Len returns the number of items across all groups.
func (i Items) Len() int {
	return len(i.Movies) + len(i.Shows) + len(i.Seasons) + len(i.Episodes) + len(i.People)
}

// chunks splits the items into bodies of at most n items, keeping AllowRewatch on each.
func (i Items) chunks(n int) []Items {
	var out []Items
	cur := Items{AllowRewatch: i.AllowRewatch}
	groups := []struct {
		items []Item
		get   func(*Items) *[]Item
	}{
		{i.Movies, func(x *Items) *[]Item { return &x.Movies }},
		{i.Shows, func(x *Items) *[]Item { return &x.Shows }},
		{i.Seasons, func(x *Items) *[]Item { return &x.Seasons }},
		{i.Episodes, func(x *Items) *[]Item { return &x.Episodes }},
		{i.People, func(x *Items) *[]Item { return &x.People }},
	}
	for _, g := range groups {
		for _, item := range g.items {
			if cur.Len() == n {
				out = append(out, cur)
				cur = Items{AllowRewatch: i.AllowRewatch}
			}
			dst := g.get(&cur)
			*dst = append(*dst, item)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur)
	}
	return out
}

// WriteResult is the outcome of a write request.
type WriteResult struct {
	Added    int
	Removed  int
	Existing int
	NotFound Items
	// Errored holds the raw "errored" objects of each response that had any.
	Errored []json.RawMessage
}

func (r *WriteResult) merge(o WriteResult) {
	r.Added += o.Added
	r.Removed += o.Removed
	r.Existing += o.Existing
	r.NotFound.Movies = append(r.NotFound.Movies, o.NotFound.Movies...)
	r.NotFound.Shows = append(r.NotFound.Shows, o.NotFound.Shows...)
	r.NotFound.Seasons = append(r.NotFound.Seasons, o.NotFound.Seasons...)
	r.NotFound.Episodes = append(r.NotFound.Episodes, o.NotFound.Episodes...)
	r.NotFound.People = append(r.NotFound.People, o.NotFound.People...)
	r.Errored = append(r.Errored, o.Errored...)
}

type writeResponse struct {
	Added    json.RawMessage `json:"added"`
	Removed  json.RawMessage `json:"removed"`
	Existing json.RawMessage `json:"existing"`
	NotFound Items           `json:"notFound"`
	Errored  json.RawMessage `json:"errored"`
}

func (w writeResponse) result() WriteResult {
	res := WriteResult{
		Added:    count(w.Added),
		Removed:  count(w.Removed),
		Existing: count(w.Existing),
		NotFound: w.NotFound,
	}
	if count(w.Errored) > 0 {
		res.Errored = append(res.Errored, w.Errored)
	}
	return res
}

// count reads a counter object from a write response. It prefers a "total" field and otherwise sums every number and
// array length it finds, because the shape of the counters differs between endpoints.
func count(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	if m, ok := v.(map[string]any); ok {
		if total, ok := m["total"].(float64); ok {
			return int(total)
		}
	}
	return sum(v)
}

func sum(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case []any:
		return len(t)
	case map[string]any:
		n := 0
		for _, x := range t {
			n += sum(x)
		}
		return n
	default:
		return 0
	}
}

// List is a WeTrakr user list.
type List struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Privacy     string `json:"privacy,omitempty"`
}

// ListInput is the body to create or update a list. Empty fields are left unchanged on update.
type ListInput struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Privacy     string `json:"privacy,omitempty"`
}

// Account is the signed-in user.
type Account struct {
	ID   int64  `json:"id"`
	Plan string `json:"plan"`
	Info struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	} `json:"info"`
}

// ExternalMatch is the result of resolving an external id.
type ExternalMatch struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Token holds the OAuth tokens of the user.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type,omitempty"`
	// ExpiresAt is computed from expires_in when the token is received.
	ExpiresAt int64 `json:"expires_at"`
}

type tokenResponse struct {
	AccessToken     string `json:"access_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
	RefreshToken    string `json:"refresh_token"`
	NewRefreshToken string `json:"new_refresh_token"`
}

// DeviceCode is the response of the device code request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// WrongAccountError means the token belongs to a different WeTrakr account than the configured username.
type WrongAccountError struct {
	Got  string
	Want string
}

func (e *WrongAccountError) Error() string {
	return fmt.Sprintf("the WeTrakr token belongs to the account %s, not to the configured username %s", e.Got, e.Want)
}

// CheckAccount returns a *WrongAccountError when username is set and the account has a different username.
func CheckAccount(a *Account, username string) error {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" || strings.EqualFold(a.Info.Username, username) {
		return nil
	}
	return &WrongAccountError{Got: a.Info.Username, Want: username}
}
