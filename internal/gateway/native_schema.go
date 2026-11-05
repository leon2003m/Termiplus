package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// describeDesktopActions fills upstream's missing conditional field descriptions.
// The endpoint, payload and validation stay native; models otherwise confuse
// click/move coordinates with the optional drag destination and repeatedly get 422.
// Open Terminal 0.16's Linux Desktop fixes its screenshot to 1280x800; the image
// smoke test checks that size. Make the pixel scale explicit for vision models
// that otherwise supply normalized 0..1000 points and repeatedly get HTTP 400.
func describeDesktopActions(response *http.Response) error {
	if response.StatusCode != http.StatusOK || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return nil
	}
	const limit = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read native schema: %w", err)
	}
	if len(body) > limit {
		response.Body = &schemaBody{Reader: io.MultiReader(bytes.NewReader(body), response.Body), Closer: response.Body}
		return nil
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(body))
	var document map[string]any
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	object := func(value any) map[string]any {
		result, _ := value.(map[string]any)
		return result
	}
	components := object(document["components"])
	schemas := object(components["schemas"])
	action := object(schemas["open_terminal__desktop__Action"])
	if action == nil {
		action = object(schemas["Action"])
	}
	fields := object(action["properties"])
	if fields == nil {
		return nil
	}
	for field, description := range map[string]string{
		"action": "click, double_click, move and drag REQUIRE both x and y. Drag ALSO requires to_x and to_y. Type requires text; key requires key.",
		"x":      "REQUIRED for click, double_click, move and drag: raw horizontal PIXEL coordinate, 0..1279 on this 1280x800 Linux desktop. Convert a normalized 0..1000 point u to round(u*1280/1000). For move this is the destination; for drag this is the start.",
		"y":      "REQUIRED for click, double_click, move and drag: raw vertical PIXEL coordinate, 0..799 on this 1280x800 Linux desktop. Convert a normalized 0..1000 point v to round(v*800/1000). The bottom panel is near y=775, NOT y=960. For move this is the destination; for drag this is the start.",
		"to_x":   "Drag destination X ONLY. For click, double_click or move use x instead; to_x does not provide their required coordinate.",
		"to_y":   "Drag destination Y ONLY. For click, double_click or move use y instead; to_y does not provide their required coordinate.",
		"text":   "Required when action is type: literal text to paste.",
	} {
		if property := object(fields[field]); property != nil {
			property["description"] = description
		}
	}
	paths := object(document["paths"])
	coordinates := "This Linux desktop returns 1280x800 screenshots. Use RAW PIXELS: x=0..1279, y=0..799, NOT normalized 0..1000 points. Convert normalized (u,v) to (round(u*1280/1000),round(v*800/1000)); for example (500,960) becomes pixel (640,768). Recalculate after an out-of-bounds error; do not repeat the failed coordinates."
	if operation := object(object(paths["/desktop/screenshot"])["get"]); operation != nil {
		original, _ := operation["description"].(string)
		operation["description"] = coordinates + "\n\n" + original
	}
	desktop := object(paths["/desktop/action"])
	if operation := object(desktop["post"]); operation != nil {
		original, _ := operation["description"].(string)
		operation["description"] = coordinates + "\nCoordinate fields: click, double_click and move require x AND y (not to_x/to_y). Example move: {\"actions\":[{\"action\":\"move\",\"x\":20,\"y\":20}]}. Example click: {\"actions\":[{\"action\":\"click\",\"x\":500,\"y\":400}]}. Drag requires x/y for the start AND to_x/to_y for the destination. Coordinates refer to the latest screenshot.\n\n" + original
	}
	updated, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode native schema: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(updated))
	response.ContentLength = int64(len(updated))
	response.Header.Set("Content-Length", strconv.Itoa(len(updated)))
	response.Header.Del("ETag")
	response.Header.Del("Content-MD5")
	return nil
}

type schemaBody struct {
	io.Reader
	io.Closer
}
