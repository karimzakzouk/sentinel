package main

// item is one thing the shop sells.
type item struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Cents int    `json:"cents"`
}

var defaults = []item{
	{ID: "apple", Name: "Apple", Cents: 50},
	{ID: "bread", Name: "Bread", Cents: 220},
	{ID: "milk", Name: "Milk", Cents: 130},
}

// load builds the catalog the shop starts with, by item ID.
func load() map[string]item {
	items := make(map[string]item, len(defaults))
	for _, it := range defaults {
		items[it.ID] = it
	}
	return items
}
