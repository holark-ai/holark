package terminalhost

import (
	"encoding/json"
	"io"
	"strconv"
)

func jsonEncoder(writer io.Writer) *json.Encoder { return json.NewEncoder(writer) }
func formatUint(value uint64) string             { return strconv.FormatUint(value, 10) }
