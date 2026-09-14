package logagent

import "testing"

func TestParseLine_FullLine(t *testing.T) {
	// Arrange
	raw := "2026-09-14T05:38:53.123456789Z stderr F connected to postgres (max_conns=20)"

	// Act
	l, err := ParseLine(raw)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l.Time != "2026-09-14T05:38:53.123456789Z" || l.Stream != "stderr" || l.Partial ||
		l.Message != "connected to postgres (max_conns=20)" {
		t.Fatalf("parsed wrong: %+v", l)
	}
}

func TestParseLine_PartialLine(t *testing.T) {
	// Arrange
	raw := "2026-09-14T05:38:53Z stdout P first half of a very long line"

	// Act
	l, err := ParseLine(raw)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !l.Partial {
		t.Fatal("expected a P line to be marked partial")
	}
}

// A container can print an empty line; that is still a valid entry.
func TestParseLine_EmptyMessage(t *testing.T) {
	for _, raw := range []string{"2026-09-14T05:38:53Z stdout F ", "2026-09-14T05:38:53Z stdout F"} {
		// Act
		l, err := ParseLine(raw)

		// Assert
		if err != nil || l.Message != "" {
			t.Fatalf("%q: want empty message and no error, got %+v, %v", raw, l, err)
		}
	}
}

func TestParseLine_RejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"",
		"hello",
		"yesterday stdout F hello",
		"2026-09-14T05:38:53Z stdin F hello",
		"2026-09-14T05:38:53Z stdout X hello",
	} {
		// Act
		_, err := ParseLine(raw)

		// Assert
		if err == nil {
			t.Fatalf("expected an error for %q", raw)
		}
	}
}

func TestParseFileName_ReadsPodNamespaceAndContainer(t *testing.T) {
	// Arrange
	path := "/var/log/containers/crm-server-86bd46676c-4dbp4_crm_crm-server-" + testID + ".log"

	// Act
	src, ok := ParseFileName(path)

	// Assert
	if !ok {
		t.Fatal("expected a valid file name")
	}
	if src.Pod != "crm-server-86bd46676c-4dbp4" || src.Namespace != "crm" || src.Container != "crm-server" {
		t.Fatalf("parsed wrong: %+v", src)
	}
}

func TestParseFileName_RejectsOtherFiles(t *testing.T) {
	for _, name := range []string{
		"random.log",
		"pod_ns.log",
		"pod_ns_container-tooshort.log",
		"pod_ns_container-" + testID + ".txt",
	} {
		// Act
		_, ok := ParseFileName(name)

		// Assert
		if ok {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestGuessLevel(t *testing.T) {
	cases := map[string]string{
		"connected to postgres":                  "info",
		"":                                       "info",
		"ERROR:  relation \"x\" does not exist":  "error",
		"PANIC in /customer.CustomerService/X":   "error",
		"FATAL:  password authentication failed": "error",
		"WARNING:  could not flush dirty data":   "warn",
	}
	for message, want := range cases {
		// Act
		got := GuessLevel(message)

		// Assert
		if got != want {
			t.Fatalf("GuessLevel(%q) = %q, want %q", message, got, want)
		}
	}
}
