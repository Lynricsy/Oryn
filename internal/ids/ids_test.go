package ids

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormats(t *testing.T) {
	assert.Regexp(t, regexp.MustCompile(`^T-[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-`), Thread())
	assert.Regexp(t, regexp.MustCompile(`^orb-[0-9a-z]{26}$`), Orb())
	assert.Regexp(t, regexp.MustCompile(`^oryn-snap-[0-9a-z]{26}$`), Snapshot())
	assert.Regexp(t, regexp.MustCompile(`^prj_[0-9A-Z]{26}$`), New("prj"))
	tok := Token("opv")
	assert.Regexp(t, regexp.MustCompile(`^opv_[A-Za-z0-9_-]{43}$`), tok)
	assert.Equal(t, "opv", TokenPrefix(tok))
	assert.Len(t, Hash(tok), 64)
}
