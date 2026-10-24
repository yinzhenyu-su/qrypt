package task

// ShareDetailKey is the Task.Detail key under which provider-backed share
// receive tasks persist their provider-neutral metadata: the link identity,
// the acting mount, the selection, both destinations, the provider wait
// handle, and the per-entry save outcome. Provider identifiers and tickets
// stay inside this key so the common task schema gains no provider fields.
const ShareDetailKey = "share"
