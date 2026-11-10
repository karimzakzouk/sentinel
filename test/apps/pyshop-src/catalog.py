"""The catalog pyshop starts with."""

DEFAULTS = [
    {"id": "apple", "name": "Apple", "cents": 50},
    {"id": "bread", "name": "Bread", "cents": 220},
    {"id": "milk", "name": "Milk", "cents": 130},
]


def load():
    """Builds the catalog by item ID."""
    items = {}
    for item in DEFAULTS:
        items[item["id"]] = item
    return items


def greeting():
    return "welcome to pyshop"
