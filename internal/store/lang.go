package store

// Language configuration for the library.
//
// These names must match the objects created in migration 00001. They exist as
// constants because getting them wrong fails silently rather than loudly: a
// query that uses a different text-search configuration than the one
// books.search_tsv was built with simply matches nothing, with no error.
//
// Changing either value is a schema change, not a config change:
//   - SearchConfig  -> rebuild books.search_tsv
//   - SortCollation -> REINDEX every index over a collated column
const (
	// SearchConfig backs books.search_tsv. Copied from Postgres' 'swedish'
	// config: this library is ~94% Swedish, and the Swedish snowball stemmer
	// folds definite and plural suffixes ("Flickorna" -> "flick") that the
	// 'simple' config leaves alone.
	SearchConfig = "library_search"

	// SortCollation orders every user-visible text column. ICU sv-SE, where
	// å, ä and ö are distinct letters sorting after z rather than variants of
	// a and o.
	SortCollation = "library_sort"
)
