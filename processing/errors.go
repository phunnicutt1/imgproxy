package processing

import (
	"fmt"
	"net/http"

	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/imagetype"
)

type (
	SaveFormatError     struct{ *errctx.TextError }
	PageOutOfRangeError struct{ *errctx.TextError }
)

func newSaveFormatError(format imagetype.Type) error {
	return SaveFormatError{errctx.NewTextError(
		fmt.Sprintf("Can't save %s, probably not supported by your libvips", format),
		1,
		errctx.WithStatusCode(http.StatusUnprocessableEntity),
		errctx.WithPublicMessage("Invalid URL"),
		errctx.WithShouldReport(false),
	)}
}

func newPageOutOfRangeError(page, pages int) error {
	msg := fmt.Sprintf("Page %d is out of range (document has %d pages)", page, pages)

	return PageOutOfRangeError{errctx.NewTextError(
		msg,
		1,
		errctx.WithStatusCode(http.StatusUnprocessableEntity),
		errctx.WithPublicMessage(msg),
		errctx.WithShouldReport(false),
	)}
}
