package dlsite

import (
	"slices"
	"testing"
)

func TestMergeFallbackMetadataReusesPublisherOnlyForSameEdition(t *testing.T) {
	source := Metadata{
		Workno:     "RJ100001",
		CircleID:   "RG12345",
		CircleName: "原作社团",
		VAs:        []string{"原作声优"},
	}
	t.Run("same edition fills publisher", func(t *testing.T) {
		work := Metadata{Workno: source.Workno}
		MergeFallbackMetadata(&work, &source)
		if work.CircleID != source.CircleID || work.CircleName != source.CircleName {
			t.Fatalf("publisher = %q (%q), want %q (%q)",
				work.CircleName, work.CircleID, source.CircleName, source.CircleID)
		}
	})

	for _, test := range []struct {
		name       string
		circleID   string
		circleName string
	}{
		{"known translated publisher", "RG54321", "翻译社团"},
		{"missing translated publisher ID", "", "翻译社团"},
		{"missing translated publisher name", "RG54321", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			translation := Metadata{
				Workno:         "RJ100002",
				OriginalWorkno: source.Workno,
				CircleID:       test.circleID,
				CircleName:     test.circleName,
			}
			MergeFallbackMetadata(&translation, &source)
			if translation.CircleID != test.circleID || translation.CircleName != test.circleName {
				t.Fatalf("publisher = %q (%q), want %q (%q)",
					translation.CircleName, translation.CircleID, test.circleName, test.circleID)
			}
			if !slices.Equal(translation.VAs, source.VAs) {
				t.Fatalf("voice credits = %v, want %v", translation.VAs, source.VAs)
			}
		})
	}
}
