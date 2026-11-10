package exercises

import (
	"context"

	"incidentctl/internal/config"
)

type Tip struct {
	Title string
	Body  string
}

type Item struct {
	Exercise    config.Exercise
	Dir         string
	Description string
	Tips        []Tip
	Err         error
}

type Source interface {
	List(context.Context) ([]Item, error)
	Get(context.Context, string) (Item, error)
}
