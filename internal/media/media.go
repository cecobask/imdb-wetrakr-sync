// Package media holds the domain types shared between the IMDb source and the WeTrakr target.
package media

import "time"

// Kind is the type of title or entity an item refers to.
type Kind string

const (
	KindMovie   Kind = "movie"
	KindShow    Kind = "show"
	KindEpisode Kind = "episode"
	KindPerson  Kind = "person"
)

// Item is a single entry of an IMDb list, the watchlist or the ratings.
type Item struct {
	// ID is the IMDb identifier: tt… for titles, nm… for people.
	ID    string
	Kind  Kind
	Title string
	// Created is when the item was added to the list, or when it was rated.
	Created time.Time
}

// Rating is a rated IMDb title. Item.Created holds the date it was rated.
type Rating struct {
	Item
	Value float64
}

// List is an IMDb user list.
type List struct {
	ID    string
	Name  string
	Items []Item
}
