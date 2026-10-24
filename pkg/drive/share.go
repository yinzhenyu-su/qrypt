package drive

import (
	"context"
	"strings"
	"time"
)

// PolicySupport describes whether a link option exists for a provider.
type PolicySupport string

const (
	PolicyUnsupported PolicySupport = "unsupported"
	PolicyOptional    PolicySupport = "optional"
	PolicyRequired    PolicySupport = "required"
)

// Share expiry values. The neutral surface accepts only these; a provider
// declares which of them it honours through SharePolicy.Expiries.
const (
	ShareExpiryPermanent time.Duration = 0
	ShareExpiryDay       time.Duration = 24 * time.Hour
	ShareExpiryWeek      time.Duration = 7 * ShareExpiryDay
	ShareExpiryMonth     time.Duration = 30 * ShareExpiryDay
)

// NeutralShareExpiries returns the expiry set the provider-neutral surface
// accepts, in a stable order.
func NeutralShareExpiries() []time.Duration {
	return []time.Duration{ShareExpiryDay, ShareExpiryWeek, ShareExpiryMonth, ShareExpiryPermanent}
}

// ShareRenameSupport declares which name-collision policies a provider can
// honour when saving into an existing directory.
type ShareRenameSupport struct {
	Auto      bool `json:"auto,omitempty"`
	Overwrite bool `json:"overwrite,omitempty"`
}

// RenamePolicy is the caller's choice for a name collision during a save.
type RenamePolicy string

const (
	// RenameFail fails the entry when the destination name exists.
	RenameFail RenamePolicy = "fail"
	// RenameAuto keeps both and lets the provider pick a new name.
	RenameAuto RenamePolicy = "auto"
	// RenameOverwrite replaces the existing destination entry.
	RenameOverwrite RenamePolicy = "overwrite"
)

// SharePolicy is what a driver declares about its link surface. The core
// validates a request against it before any provider call, and the CLI renders
// help and rejects unsupported options from the same declaration, so a
// provider that lacks an option never silently drops it.
type SharePolicy struct {
	Passcode   PolicySupport   `json:"passcode"`
	Expiries   []time.Duration `json:"expiries,omitempty"`
	VisitLimit bool            `json:"visit_limit,omitempty"`
	Update     bool            `json:"update,omitempty"`
	List       bool            `json:"list,omitempty"`
	// Selection means the provider exposes enough metadata (names and sizes)
	// for the neutral all/largest/glob selection to be applied. A provider that
	// cannot enumerate that metadata rejects any selector other than all.
	Selection bool               `json:"selection,omitempty"`
	Rename    ShareRenameSupport `json:"rename,omitempty"`
}

// SupportsExpiry reports whether the provider accepts an expiry value.
func (p SharePolicy) SupportsExpiry(expiry time.Duration) bool {
	for _, allowed := range p.Expiries {
		if allowed == expiry {
			return true
		}
	}
	return false
}

// SupportsRename reports whether the provider can honour a collision policy.
// RenameFail needs no provider support: refusing to touch an existing entry is
// always available.
func (p SharePolicy) SupportsRename(policy RenamePolicy) bool {
	switch policy {
	case RenameFail, "":
		return true
	case RenameAuto:
		return p.Rename.Auto
	case RenameOverwrite:
		return p.Rename.Overwrite
	default:
		return false
	}
}

// ValidateCreateRequest rejects create options the policy does not declare, so
// a caller never asks a provider for something it would silently drop.
func (p SharePolicy) ValidateCreateRequest(req ShareCreateRequest) error {
	switch p.Passcode {
	case PolicyRequired:
		if strings.TrimSpace(req.Passcode) == "" {
			return NewShareError(ShareErrUnsupported, "", "this provider requires a passcode")
		}
	case PolicyUnsupported:
		if strings.TrimSpace(req.Passcode) != "" {
			return NewShareError(ShareErrUnsupported, "", "this provider does not support passcodes")
		}
	}
	if !p.SupportsExpiry(req.Expiry) {
		return NewShareError(ShareErrUnsupported, "", "this provider does not accept that expiry")
	}
	if req.VisitLimit > 0 && !p.VisitLimit {
		return NewShareError(ShareErrUnsupported, "", "this provider does not support visit limits")
	}
	return nil
}

// ValidateUpdateRequest rejects update options the policy does not declare.
func (p SharePolicy) ValidateUpdateRequest(req ShareUpdateRequest) error {
	if !p.Update {
		return NewShareError(ShareErrUnsupported, "", "this provider does not support updating links")
	}
	if req.Passcode != nil {
		switch p.Passcode {
		case PolicyUnsupported:
			return NewShareError(ShareErrUnsupported, "", "this provider does not support passcodes")
		case PolicyRequired:
			if strings.TrimSpace(*req.Passcode) == "" {
				return NewShareError(ShareErrUnsupported, "", "this provider requires a passcode")
			}
		}
	}
	if req.Public != nil && *req.Public && p.Passcode == PolicyRequired {
		return NewShareError(ShareErrUnsupported, "", "this provider always requires a passcode")
	}
	if req.Expiry != nil && !p.SupportsExpiry(*req.Expiry) {
		return NewShareError(ShareErrUnsupported, "", "this provider does not accept that expiry")
	}
	return nil
}

// ValidateListRequest reports whether the provider can enumerate its links.
func (p SharePolicy) ValidateListRequest(_ ShareListRequest) error {
	if !p.List {
		return NewShareError(ShareErrUnsupported, "", "this provider does not list links")
	}
	return nil
}

// ValidateSaveRequest rejects a destination collision policy the provider
// cannot honour.
func (p SharePolicy) ValidateSaveRequest(req ShareSaveRequest) error {
	if !p.SupportsRename(req.Dest.Rename) {
		return NewShareError(ShareErrUnsupported, "", "this provider cannot honour that rename policy")
	}
	return nil
}

// SharePolicyReporter is an optional interface a provider implements to
// declare its link surface.
type SharePolicyReporter interface {
	SharePolicy() SharePolicy
}

// SharePolicyFor reports a driver's declared share policy. A driver that does
// not implement SharePolicyReporter, or declares no share capability at all,
// returns the zero policy and therefore supports no option. Any of the four
// share capabilities qualifies: a receive-only provider still declares
// passcode and selection policy without offering link management.
func SharePolicyFor(d Driver) SharePolicy {
	if d == nil || !hasAnyShareCapability(d) {
		return SharePolicy{}
	}
	reporter, ok := d.(SharePolicyReporter)
	if !ok {
		return SharePolicy{}
	}
	return reporter.SharePolicy()
}

// hasAnyShareCapability reports whether a driver takes part in the share
// contract in any direction.
func hasAnyShareCapability(d Driver) bool {
	for _, capability := range []Capability{
		CapabilityShare, CapabilityShareReceive, CapabilityShareSave, CapabilityShareFetch,
	} {
		if HasCapability(d, capability) {
			return true
		}
	}
	return false
}

// ShareLink is a provider-neutral share link. LinkID is the identity used for
// de-duplication and task detail; URL is display-only because the same link is
// reachable through several hosts.
type ShareLink struct {
	URL      string `json:"url"`
	LinkID   string `json:"link_id"`
	Passcode string `json:"passcode,omitempty"`
}

// ShareOwner describes who published a link.
type ShareOwner struct {
	Name       string `json:"name,omitempty"`
	MemberType string `json:"member_type,omitempty"`
	AvatarURL  string `json:"avatar_url,omitempty"`
}

// Share is one link's metadata.
type Share struct {
	NativeID   string     `json:"native_id"`
	LinkID     string     `json:"link_id"`
	URL        string     `json:"url"`
	Title      string     `json:"title,omitempty"`
	Owner      ShareOwner `json:"owner,omitempty"`
	Public     bool       `json:"public,omitempty"`
	Passcode   string     `json:"passcode,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at,omitempty"`
	VisitLimit int        `json:"visit_limit,omitempty"`
	VisitCount int        `json:"visit_count,omitempty"`
	EntryCount int        `json:"entry_count,omitempty"`
	Size       int64      `json:"size,omitempty"`
	CreatedAt  time.Time  `json:"created_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at,omitempty"`
	Risk       string     `json:"risk,omitempty"`
}

// ShareSession is an opened link: the link itself plus the provider's opaque
// session token for listing and saving. Callers must not persist it. ExpiresAt may be zero when the provider does
// not disclose an expiry.
type ShareSession struct {
	Link         ShareLink `json:"link"`
	SessionToken string    `json:"session_token"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Share        Share     `json:"share"`
}

// SharePath addresses a directory inside a share. ID is the share-relative
// directory identity ("0" is the share root); Path is an optional relative
// path for providers that address by name.
type SharePath struct {
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
}

// ShareEntry is one entry inside a share. Ticket is an opaque per-entry save
// ticket (Quark's share_fid_token); providers that need none leave it empty.
type ShareEntry struct {
	Entry
	Ticket string `json:"ticket,omitempty"`
	RootID string `json:"root_id,omitempty"`
}

// ShareEntryPage is one page of a share listing.
type ShareEntryPage struct {
	Entries []ShareEntry `json:"entries,omitempty"`
	Next    string       `json:"next,omitempty"`
}

// ShareCreateRequest asks a provider to publish a link for existing entries.
type ShareCreateRequest struct {
	Entries    []Entry       `json:"entries"`
	Title      string        `json:"title,omitempty"`
	Expiry     time.Duration `json:"expiry,omitempty"`
	Passcode   string        `json:"passcode,omitempty"`
	VisitLimit int           `json:"visit_limit,omitempty"`
}

// ShareListRequest pages through the acting account's links. Cursor is opaque.
type ShareListRequest struct {
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeExpired bool   `json:"include_expired,omitempty"`
}

// SharePage is one page of the acting account's links.
type SharePage struct {
	Shares []Share `json:"shares,omitempty"`
	Next   string  `json:"next,omitempty"`
}

// ShareUpdateRequest changes a link's policy. Nil fields stay unchanged.
type ShareUpdateRequest struct {
	ID       string         `json:"id"`
	Title    *string        `json:"title,omitempty"`
	Expiry   *time.Duration `json:"expiry,omitempty"`
	Passcode *string        `json:"passcode,omitempty"`
	Public   *bool          `json:"public,omitempty"`
}

// ShareDestination is where a save writes: the resolved destination directory
// (Entry.Extra may carry a drive or space identity) plus the collision policy.
type ShareDestination struct {
	Entry  Entry        `json:"entry"`
	Rename RenamePolicy `json:"rename,omitempty"`
}

// ShareSaveRequest asks a provider to copy share entries into the user's drive.
type ShareSaveRequest struct {
	Session ShareSession     `json:"session"`
	Entries []ShareEntry     `json:"entries"`
	Dest    ShareDestination `json:"dest"`
}

// Normalize fills defaults that do not depend on the provider.
func (r *ShareSaveRequest) Normalize() {
	if r.Dest.Rename == "" {
		r.Dest.Rename = RenameFail
	}
}

// SaveState is a provider-side save's lifecycle position.
type SaveState string

const (
	SaveStateQueued    SaveState = "queued"
	SaveStateRunning   SaveState = "running"
	SaveStateSucceeded SaveState = "succeeded"
	SaveStatePartial   SaveState = "partial"
	SaveStateFailed    SaveState = "failed"
)

// ShareSaveItem is one entry's outcome. Saved is nil when the entry failed.
type ShareSaveItem struct {
	SourceID   string      `json:"source_id"`
	SourceName string      `json:"source_name,omitempty"`
	SourceSize int64       `json:"source_size,omitempty"`
	Saved      *Entry      `json:"saved,omitempty"`
	Error      *ShareError `json:"error,omitempty"`
}

// ShareSaveOutcome is a provider save result. Handle is empty when the provider
// finished inside the request; otherwise the core polls GetShareSave with it.
// Progress is best effort and may stay zero.
type ShareSaveOutcome struct {
	Handle   string          `json:"handle,omitempty"`
	State    SaveState       `json:"state"`
	Progress float64         `json:"progress,omitempty"`
	Items    []ShareSaveItem `json:"items,omitempty"`
}

// Complete reports whether the outcome is terminal.
func (o ShareSaveOutcome) Complete() bool {
	return o.State == SaveStateSucceeded || o.State == SaveStatePartial || o.State == SaveStateFailed
}

// DownloadTarget is a provider-signed download URL plus any headers the
// request needs. ExpiresAt may be zero when the provider does not disclose an
// expiry; the caller then re-requests on transfer failure rather than
// pre-emptively.
type DownloadTarget struct {
	URL       string            `json:"url"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// ShareFetchRequest asks a provider for download targets for share content.
type ShareFetchRequest struct {
	Session ShareSession `json:"session"`
	Entries []ShareEntry `json:"entries"`
}

// ShareLinkParser is an optional provider capability: recognize the provider's
// own share-link URLs (and any short form it accepts) so callers never need to
// name a provider.
type ShareLinkParser interface {
	ParseShareLink(raw string) (ShareLink, bool)
}

// ShareManager is an optional provider capability for links the acting account
// owns. It is declared with CapabilityShare; individual methods may still
// return a ShareError with ShareErrUnsupported for options or operations the
// policy does not declare.
type ShareManager interface {
	CreateShare(ctx context.Context, req ShareCreateRequest) (Share, error)
	ListShares(ctx context.Context, req ShareListRequest) (SharePage, error)
	GetShare(ctx context.Context, id string) (Share, error)
	UpdateShare(ctx context.Context, req ShareUpdateRequest) (Share, error)
	DeleteShare(ctx context.Context, id string) error
}

// ShareReceiver is an optional provider capability: open a link and browse it.
// It is declared with CapabilityShareReceive.
type ShareReceiver interface {
	OpenShare(ctx context.Context, link ShareLink) (ShareSession, error)
	ListShareEntries(ctx context.Context, session ShareSession, dir SharePath, cursor string) (ShareEntryPage, error)
}

// ShareSaver is an optional provider capability: copy share content into the
// user's drive. It is declared with CapabilityShareSave. Implementations may
// complete synchronously (Handle empty) or asynchronously (poll GetShareSave).
type ShareSaver interface {
	SaveShareEntries(ctx context.Context, req ShareSaveRequest) (ShareSaveOutcome, error)
	// GetShareSave polls a provider save. Entries are the source entries the
	// save was asked to copy; passing them lets a provider that reports only
	// destination FIDs still return one result per selected entry, including
	// after a process restart when provider-side state is all that remains.
	// Callers pass the same selection they submitted; providers may ignore it.
	GetShareSave(ctx context.Context, handle string, entries []ShareEntry) (ShareSaveOutcome, error)
}

// ShareFetcher is an optional provider capability: hand out download targets
// for share content without saving it. It is declared with
// CapabilityShareFetch; targets may require provider-specific headers. The
// returned targets MUST be in the same order and length as the request's
// entries.
type ShareFetcher interface {
	ShareDownloadTargets(ctx context.Context, req ShareFetchRequest) ([]DownloadTarget, error)
}
