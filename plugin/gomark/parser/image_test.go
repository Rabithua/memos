package parser

import (
	"testing"

	"github.com/rabithua/memos/plugin/gomark/parser/tokenizer"
	"github.com/stretchr/testify/require"
)

func TestImageParser(t *testing.T) {
	tests := []struct {
		text  string
		image *ImageParser
	}{
		{
			text: "![](https://example.com)",
			image: &ImageParser{
				AltText: "",
				URL:     "https://example.com",
			},
		},
		{
			text:  "! [](https://example.com)",
			image: nil,
		},
		{
			text:  "![alte]( htt ps :/ /example.com)",
			image: nil,
		},
		{
			text: "![al te](https://example.com)",
			image: &ImageParser{
				AltText: "al te",
				URL:     "https://example.com",
			},
		},
	}
	for _, test := range tests {
		tokens := tokenizer.Tokenize(test.text)
		require.Equal(t, test.image, NewImageParser().Match(tokens))
	}
}
