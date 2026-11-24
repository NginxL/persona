package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A message within its limits writes nothing; one over says in the request's language how many pictures it had and
// how many fit, with the numbers beside the words.
func TestImagesOverLimit(t *testing.T) {
	l := proto.DefaultImageLimits
	rec := httptest.NewRecorder()
	if ImagesOverLimit(rec, l, l.PerMessage) || rec.Body.Len() != 0 {
		t.Fatalf("a message of %d pictures was refused: %s", l.PerMessage, rec.Body)
	}
	want := map[i18n.Lang]string{
		i18n.En:     "This message has 23 pictures; a message holds up to 20.",
		i18n.ZhHans: "这条消息有 23 张图，一条最多 20 张。",
		i18n.Ja:     "このメッセージには画像が23枚あります。1件に入れられるのは20枚までです。",
	}
	for lang, words := range want {
		rec := httptest.NewRecorder()
		if !ImagesOverLimit(langWriter{rec, lang}, l, 23) {
			t.Fatalf("%s: 23 pictures were not refused", lang)
		}
		var body struct {
			Error, Code, Delivered string
			Retryable              bool
			Limit                  proto.ImageLimits
			Got                    struct{ Images int }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusRequestEntityTooLarge ||
			body.Error != words || body.Code != proto.CodeImagesOverLimit || body.Delivered != "none" || body.Retryable ||
			body.Limit != l || body.Got.Images != 23 {
			t.Errorf("%s: %d %s", lang, rec.Code, rec.Body)
		}
	}
}
