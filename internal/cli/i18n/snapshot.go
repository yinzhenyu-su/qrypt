package i18n

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const englishAnnotationPrefix = "qrypt.i18n.en."

// snapshotCommandField records the pre-localization text for one command field.
// The snapshot lets machine-readable output reconstruct English after the
// command tree has been localized in place.
func snapshotCommandField(cmd *cobra.Command, field, value string) {
	if cmd == nil || value == "" {
		return
	}
	key := englishAnnotationPrefix + field
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	if _, ok := cmd.Annotations[key]; !ok {
		cmd.Annotations[key] = value
	}
}

// EnglishCommandField returns the stored pre-localization command text.
func EnglishCommandField(cmd *cobra.Command, field string) (string, bool) {
	if cmd == nil {
		return "", false
	}
	value, ok := cmd.Annotations[englishAnnotationPrefix+field]
	return value, ok
}

// snapshotFlagUsage records a flag's pre-localization usage text.
func snapshotFlagUsage(flag *pflag.Flag) {
	if flag == nil || flag.Usage == "" {
		return
	}
	key := englishAnnotationPrefix + "usage"
	if flag.Annotations == nil {
		flag.Annotations = map[string][]string{}
	}
	if _, ok := flag.Annotations[key]; !ok {
		flag.Annotations[key] = []string{flag.Usage}
	}
}

// EnglishFlagUsage returns the stored pre-localization flag usage.
func EnglishFlagUsage(flag *pflag.Flag) (string, bool) {
	if flag == nil {
		return "", false
	}
	values, ok := flag.Annotations[englishAnnotationPrefix+"usage"]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}
