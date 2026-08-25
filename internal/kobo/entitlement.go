package kobo

import (
	"fmt"
	"strings"
	"time"
)

// The JSON below mirrors what a Kobo device expects from the official store
// API. Field names and casing are load-bearing: the firmware ignores anything
// it does not recognise and misbehaves on anything it half-recognises, so this
// follows calibre-web's proven shapes rather than inventing tidier ones.

// BookEntitlement is the device's licence to hold a book.
type BookEntitlement struct {
	Accessibility string `json:"Accessibility"`
	ActivePeriod  struct {
		From string `json:"From"`
	} `json:"ActivePeriod"`
	Created             string `json:"Created"`
	CrossRevisionId     string `json:"CrossRevisionId"`
	Id                  string `json:"Id"`
	IsRemoved           bool   `json:"IsRemoved"`
	IsHiddenFromArchive bool   `json:"IsHiddenFromArchive"`
	IsLocked            bool   `json:"IsLocked"`
	LastModified        string `json:"LastModified"`
	OriginCategory      string `json:"OriginCategory"`
	RevisionId          string `json:"RevisionId"`
	Status              string `json:"Status"`
}

// ContributorRole names an author in the shape the device reads.
type ContributorRole struct {
	Name string `json:"Name"`
}

// DownloadURL tells the device where to fetch a format.
type DownloadURL struct {
	Format   string `json:"Format"`
	Size     int64  `json:"Size"`
	Url      string `json:"Url"`
	Platform string `json:"Platform"`
	DrmType  string `json:"DrmType"`
}

// SeriesInfo describes a book's place in a series.
type SeriesInfo struct {
	Name        string  `json:"Name"`
	Number      int     `json:"Number,omitempty"`
	NumberFloat float64 `json:"NumberFloat,omitempty"`
	Id          string  `json:"Id"`
}

// BookMetadata is everything the device shows about a book.
type BookMetadata struct {
	Categories          []string          `json:"Categories"`
	Contributors        []string          `json:"Contributors,omitempty"`
	ContributorRoles    []ContributorRole `json:"ContributorRoles,omitempty"`
	CoverImageId        string            `json:"CoverImageId"`
	CrossRevisionId     string            `json:"CrossRevisionId"`
	CurrentDisplayPrice struct {
		CurrencyCode string  `json:"CurrencyCode"`
		TotalAmount  float64 `json:"TotalAmount"`
	} `json:"CurrentDisplayPrice"`
	CurrentLoveDisplayPrice struct {
		TotalAmount float64 `json:"TotalAmount"`
	} `json:"CurrentLoveDisplayPrice"`
	Description            string        `json:"Description,omitempty"`
	DownloadUrls           []DownloadURL `json:"DownloadUrls"`
	EntitlementId          string        `json:"EntitlementId"`
	ExternalIds            []string      `json:"ExternalIds"`
	Genre                  string        `json:"Genre"`
	IsEligibleForKoboLove  bool          `json:"IsEligibleForKoboLove"`
	IsInternetArchive      bool          `json:"IsInternetArchive"`
	IsPreOrder             bool          `json:"IsPreOrder"`
	IsSocialEnabled        bool          `json:"IsSocialEnabled"`
	Language               string        `json:"Language"`
	PhoneticPronunciations struct{}      `json:"PhoneticPronunciations"`
	PublicationDate        string        `json:"PublicationDate,omitempty"`
	Publisher              *struct {
		Imprint string `json:"Imprint"`
		Name    string `json:"Name"`
	} `json:"Publisher,omitempty"`
	RevisionId string      `json:"RevisionId"`
	Series     *SeriesInfo `json:"Series,omitempty"`
	Title      string      `json:"Title"`
	WorkId     string      `json:"WorkId"`
}

// NewEntitlement is emitted for a book the device has not seen.
type NewEntitlement struct {
	BookEntitlement BookEntitlement `json:"BookEntitlement"`
	BookMetadata    BookMetadata    `json:"BookMetadata"`
	ReadingState    *ReadingState   `json:"ReadingState,omitempty"`
}

// ChangedEntitlement is emitted for a book whose metadata or status moved.
type ChangedEntitlement struct {
	BookEntitlement BookEntitlement `json:"BookEntitlement"`
	BookMetadata    BookMetadata    `json:"BookMetadata"`
	ReadingState    *ReadingState   `json:"ReadingState,omitempty"`
}

// StatusInfo is where the device is in a book.
type StatusInfo struct {
	LastModified           string `json:"LastModified"`
	Status                 string `json:"Status"`
	TimesStartedReading    int    `json:"TimesStartedReading"`
	LastTimeStartedReading string `json:"LastTimeStartedReading,omitempty"`
	LastTimeFinished       string `json:"LastTimeFinished,omitempty"`
}

// Location is a position within a book.
type Location struct {
	Source string `json:"Source,omitempty"`
	Type   string `json:"Type,omitempty"`
	Value  string `json:"Value,omitempty"`
}

// Bookmark is the current reading position.
type Bookmark struct {
	ContentSourceProgressPercent int      `json:"ContentSourceProgressPercent"`
	LastModified                 string   `json:"LastModified"`
	Location                     Location `json:"Location"`
	ProgressPercent              int      `json:"ProgressPercent"`
}

// Statistics is reading time.
type Statistics struct {
	LastModified         string `json:"LastModified"`
	RemainingTimeMinutes int    `json:"RemainingTimeMinutes,omitempty"`
	SpentReadingMinutes  int    `json:"SpentReadingMinutes,omitempty"`
}

// ReadingState is Kobo's per-book progress object.
type ReadingState struct {
	Created           string     `json:"Created"`
	CurrentBookmark   Bookmark   `json:"CurrentBookmark"`
	EntitlementId     string     `json:"EntitlementId"`
	LastModified      string     `json:"LastModified"`
	PriorityTimestamp string     `json:"PriorityTimestamp"`
	Statistics        Statistics `json:"Statistics"`
	StatusInfo        StatusInfo `json:"StatusInfo"`
}

// ChangedReadingState wraps a progress update.
type ChangedReadingState struct {
	ReadingState ReadingState `json:"ReadingState"`
}

// Tag is a shelf as the device sees it: a Collection.
type Tag struct {
	Created      string    `json:"Created"`
	Id           string    `json:"Id"`
	Items        []TagItem `json:"Items,omitempty"`
	LastModified string    `json:"LastModified"`
	Name         string    `json:"Name"`
	Type         string    `json:"Type"`
}

// TagItem is one book in a collection.
type TagItem struct {
	RevisionId string `json:"RevisionId"`
	Type       string `json:"Type"`
}

// NewTag, ChangedTag and DeletedTag are the collection sync entities.
type NewTag struct {
	Tag Tag `json:"Tag"`
}
type ChangedTag struct {
	Tag Tag `json:"Tag"`
}
type DeletedTag struct {
	Tag struct {
		Id string `json:"Id"`
	} `json:"Tag"`
}

// koboTime formats a timestamp the way the device expects.
func koboTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// koboDate formats a publication date.
func koboDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// downloadURL builds the absolute URL the device fetches a book from.
//
// It must be absolute and must use the externally reachable host: the device
// resolves it itself and has no idea what the container is called. It must also
// be https -- Kobo firmware silently fails on a plain-http download when the
// store URL it was configured with is https.
func downloadURL(externalURL, token string, bookID int64, format string) string {
	return fmt.Sprintf("%s/kobo/%s/download/%d/%s",
		strings.TrimRight(externalURL, "/"), token, bookID, strings.ToLower(format))
}

// imageURL builds the cover URL for a book.
func imageURL(externalURL, token, uuid string, w, h int) string {
	return fmt.Sprintf("%s/kobo/%s/%s/%d/%d/false/image.jpg",
		strings.TrimRight(externalURL, "/"), token, uuid, w, h)
}
