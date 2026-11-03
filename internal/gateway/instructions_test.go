package gateway

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInstructionsShape(t *testing.T) {
	got := Instructions([]ServerInfo{
		{Name: "a", About: "A about", Tools: []ToolInfo{
			{Name: "x", Description: "First sentence. Second sentence."},
			{Name: "y"},
		}},
		{Name: "b", About: "B about", Tools: []ToolInfo{
			{Name: "z", Description: "Spans\n  two lines.\nLine two"},
			{Name: "w", Description: "中文第一句。第二句"},
		}},
		// A server without tools is named in the first line but gets no line.
		{Name: "c"},
	})
	want := "Tools of a (A about), b (B about), c.\n" +
		"- a: Tools: x (First sentence.), y\n" +
		"- b: Tools: z (Spans two lines.), w (中文第一句。)"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if Instructions(nil) != "" {
		t.Fatal("no servers should give empty instructions")
	}
}

func TestInstructionsAboutTruncation(t *testing.T) {
	about := strings.Repeat("alpha ", 60)
	got := Instructions([]ServerInfo{{Name: "a", About: about}, {Name: "b", About: about}})
	if !regexp.MustCompile(`^Tools of a \((alpha )+alpha…\), b \((alpha )+alpha…\)\.$`).MatchString(got) {
		t.Fatalf("got %q", got)
	}
	if n := utf8.RuneCountInString(got); n > firstLineBudget || n < firstLineBudget-20 {
		t.Fatalf("first line length %d", n)
	}
}

func TestInstructionsLimit(t *testing.T) {
	desc := strings.Repeat("lorem ipsum dolor sit amet ", 8) + "end. More."
	var servers []ServerInfo
	for i := range 40 {
		s := ServerInfo{Name: fmt.Sprintf("server%02d", i), About: "About server number " + strconv.Itoa(i) + " doing many things"}
		for j := range 30 {
			s.Tools = append(s.Tools, ToolInfo{Name: fmt.Sprintf("tool_number_%02d", j), Description: desc})
		}
		servers = append(servers, s)
	}
	got := Instructions(servers)
	if n := utf8.RuneCountInString(got); n > maxInstructions {
		t.Fatalf("length %d", n)
	}
	lines := strings.Split(got, "\n")
	if utf8.RuneCountInString(lines[0]) > firstLineBudget {
		t.Fatalf("first line too long: %q", lines[0])
	}
	m := regexp.MustCompile(`, and (\d+) more\.$`).FindStringSubmatch(lines[0])
	if m == nil {
		t.Fatalf("first line %q", lines[0])
	}
	listed := len(lines) - 1
	if n, _ := strconv.Atoi(m[1]); listed == 0 || n != 40-listed || strings.Count(lines[0], "server") != listed {
		t.Fatalf("listed %d, more %s: %q", listed, m[1], lines[0])
	}
	tail := regexp.MustCompile(`, and (\d+) more$`)
	for _, l := range lines[1:] {
		if strings.Contains(l, "(") {
			t.Fatalf("expected names only: %q", l)
		}
		m := tail.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("expected and N more: %q", l)
		}
		shown := strings.Count(l, "tool_number_")
		if n, _ := strconv.Atoi(m[1]); shown == 0 || n != 30-shown {
			t.Fatalf("shown %d, more %s: %q", shown, m[1], l)
		}
	}
}

func TestInstructionsNamesOnlyFallback(t *testing.T) {
	desc := strings.Repeat("word ", 100) + "."
	var tools []ToolInfo
	for j := range 10 {
		tools = append(tools, ToolInfo{Name: fmt.Sprintf("t%d", j), Description: desc})
	}
	got := Instructions([]ServerInfo{{Name: "a", Tools: tools}})
	want := "Tools of a.\n- a: Tools: t0, t1, t2, t3, t4, t5, t6, t7, t8, t9"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestInstructionsMultibyteTruncation(t *testing.T) {
	var servers []ServerInfo
	for i := range 12 {
		s := ServerInfo{Name: fmt.Sprintf("中文服务%d", i), About: strings.Repeat("这是一个非常长的中文描述", 10)}
		for j := range 40 {
			s.Tools = append(s.Tools, ToolInfo{Name: fmt.Sprintf("工具%d", j), Description: strings.Repeat("中文工具描述", 30) + "。"})
		}
		servers = append(servers, s)
	}
	got := Instructions(servers)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) > maxInstructions {
		t.Fatalf("valid=%v len=%d", utf8.ValidString(got), utf8.RuneCountInString(got))
	}
	first := strings.SplitN(got, "\n", 2)[0]
	if strings.Count(first, "…") != 12 || strings.Contains(first, "more") {
		t.Fatalf("first line %q", first)
	}
}

func TestAbout(t *testing.T) {
	info := &mcp.InitializeResult{
		ServerInfo:   &mcp.Implementation{Name: "x", Description: " server desc "},
		Instructions: "\n first line \nsecond",
	}
	cases := []struct {
		cfg  string
		info *mcp.InitializeResult
		want string
	}{
		{" cfg ", info, "cfg"},
		{"", info, "server desc"},
		{"", &mcp.InitializeResult{Instructions: "  first line \nsecond"}, "first line"},
		{"", nil, ""},
		{"", &mcp.InitializeResult{}, ""},
	}
	for _, c := range cases {
		if got := About(c.cfg, c.info); got != c.want {
			t.Errorf("About(%q) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestInstructionsBoundsExtremes(t *testing.T) {
	name := strings.Repeat("n", 32)
	for _, n := range []int{1, 3, 10, 50, 100, 300} {
		var servers []ServerInfo
		for i := range n {
			s := ServerInfo{Name: name[:28] + fmt.Sprintf("%04d", i), About: strings.Repeat("长", 300)}
			for j := range 200 {
				s.Tools = append(s.Tools, ToolInfo{Name: strings.Repeat("t", 60) + fmt.Sprint(j), Description: strings.Repeat("d", 900)})
			}
			servers = append(servers, s)
		}
		got := Instructions(servers)
		first := strings.SplitN(got, "\n", 2)[0]
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) > maxInstructions || utf8.RuneCountInString(first) > firstLineBudget {
			t.Fatalf("n=%d: len %d, first %d", n, utf8.RuneCountInString(got), utf8.RuneCountInString(first))
		}
	}
}

func TestTruncateWords(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"alpha beta", 20, "alpha beta"},
		{"multi\n line  about", 20, "multi line about"},
		{"alpha beta gamma", 11, "alpha beta…"},
		{"alpha beta gamma", 13, "alpha beta…"},
		{"使用 tapd 查询需求单和缺陷单", 10, "使用 tapd 查…"},
		{"averylongword", 5, "aver…"},
		{"abc def", 1, ""},
	}
	for _, c := range cases {
		if got := truncateWords(c.in, c.n); got != c.want {
			t.Errorf("truncateWords(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestInstructionsSingleLines(t *testing.T) {
	got := Instructions([]ServerInfo{{Name: "a", About: "multi\nline", Tools: []ToolInfo{{Name: "x\ny", Description: "d"}}}})
	if want := "Tools of a (multi line).\n- a: Tools: x y (d)"; got != want {
		t.Fatalf("got %q", got)
	}
}
