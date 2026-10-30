package sharedkey

import "testing"

func TestMeterFragmentedSSEAndRepeatedUsage(t *testing.T) {
	m := newMeter("/responses")
	stream := "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":3}}}}\r\n\r\ndata: [DONE]\r\n\r\n"
	for _, b := range []byte(stream) {
		m.feed([]byte{b})
	}
	m.consume([]byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`))
	if !m.complete || !m.seen || m.invalid || m.total() != 15 || m.tokens.CachedTokens != 3 {
		t.Fatalf("meter=%+v", m)
	}
}

func TestMeterRejectsMissingMalformedOrNegativeUsage(t *testing.T) {
	for _, data := range []string{`{"usage":{"output_tokens":5}}`, `{"usage":{"input_tokens":null,"output_tokens":5}}`, `{"usage":{"input_tokens":-1,"output_tokens":5}}`, `{"usage":{"input_tokens":1,"output_tokens":"5"}}`, `{"usage":{"input_tokens":1e20,"output_tokens":5}}`} {
		m := newMeter("/chat/completions")
		m.consume([]byte(data))
		if m.seen && !m.invalid {
			t.Fatalf("accepted incomplete usage: %s", data)
		}
	}
}
