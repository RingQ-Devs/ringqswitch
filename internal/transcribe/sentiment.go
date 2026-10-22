package transcribe

import (
	_ "embed"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Sentiment: the VADER method (Hutto & Gilbert, 2014), rule-based and
// tuned for short, informal sentences, which is what a call's utterances
// are. Each word's valence comes from the lexicon; boosters ("very"),
// dampeners ("slightly"), negation ("not", "never"...), "but", "least",
// capitals and exclamation marks adjust it; the sum is normalised to a
// compound score from -1 (most negative) to +1 (most positive).

//go:embed vader_lexicon.txt
var vaderLexicon string

// Words scored differently on calls: an agent's "sorry" is an apology,
// a caller's "sorry?" a request to repeat, neither negative.
var callOverrides = map[string]float64{"sorry": 0}

// Call centre words the general lexicon leaves out (our additions).
var callLexicon = map[string]float64{
	"overcharged": -2.0, "overcharge": -1.8, "escalate": -1.0, "escalation": -1.0, "unresolved": -1.6,
	"outage": -1.8, "chargeback": -1.6, "refund": -0.4, "cancellation": -0.6, "disconnected": -1.0,
	"unreachable": -1.2,
}

const (
	bIncr   = 0.293
	bDecr   = -0.293
	cIncr   = 0.733
	nScalar = -0.74
)

var negateWords = toSet("aint", "arent", "cannot", "cant", "couldnt", "darent", "didnt", "doesnt",
	"ain't", "aren't", "can't", "couldn't", "daren't", "didn't", "doesn't",
	"dont", "hadnt", "hasnt", "havent", "isnt", "mightnt", "mustnt", "neither",
	"don't", "hadn't", "hasn't", "haven't", "isn't", "mightn't", "mustn't",
	"neednt", "needn't", "never", "none", "nope", "nor", "not", "nothing", "nowhere",
	"oughtnt", "shant", "shouldnt", "uhuh", "wasnt", "werent",
	"oughtn't", "shan't", "shouldn't", "uh-uh", "wasn't", "weren't",
	"without", "wont", "wouldnt", "won't", "wouldn't", "rarely", "seldom", "despite")

var boosters = func() map[string]float64 {
	m := map[string]float64{}
	for _, w := range strings.Fields(`absolutely amazingly awfully completely considerable considerably decidedly deeply
		effing enormous enormously entirely especially exceptional exceptionally extreme extremely fabulously flipping
		flippin frackin fracking fricking frickin frigging friggin fully fuckin fucking fuggin fugging greatly hella
		highly hugely incredible incredibly intensely major majorly more most particularly purely quite really
		remarkably so substantially thoroughly total totally tremendous tremendously uber unbelievably unusually
		utter utterly very`) {
		m[w] = bIncr
	}
	for _, w := range []string{"almost", "barely", "hardly", "just enough", "kind of", "kinda", "kindof", "kind-of",
		"less", "little", "marginal", "marginally", "occasional", "occasionally", "partly", "scarce", "scarcely",
		"slight", "slightly", "somewhat", "sort of", "sorta", "sortof", "sort-of"} {
		m[w] = bDecr
	}
	return m
}()

var specialCases = map[string]float64{"the shit": 3, "the bomb": 3, "bad ass": 1.5, "badass": 1.5, "bus stop": 0.0,
	"yeah right": -2, "kiss of death": -1.5, "to die for": 3, "beating heart": 3.5}

var lexicon = func() map[string]float64 {
	m := make(map[string]float64, 7600)
	for _, line := range strings.Split(vaderLexicon, "\n") {
		w, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			m[w] = f
		}
	}
	for w, v := range callLexicon {
		if _, ok := m[w]; !ok && v != 0 {
			m[w] = v
		}
	}
	for w, v := range callOverrides {
		if v == 0 {
			delete(m, w)
		} else {
			m[w] = v
		}
	}
	return m
}()

func toSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// Sentiment returns the compound score of a sentence (-1 to +1).
func Sentiment(text string) float64 {
	words := sentiWords(text)
	if len(words) == 0 {
		return 0
	}
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	capDiff := allCapDifferential(words)
	vals := make([]float64, 0, len(words))
	for i := range words {
		if _, ok := boosters[lower[i]]; ok {
			vals = append(vals, 0)
			continue
		}
		if i < len(words)-1 && lower[i] == "kind" && lower[i+1] == "of" {
			vals = append(vals, 0)
			continue
		}
		vals = append(vals, valence(words, lower, i, capDiff))
	}
	// "but": what comes before counts half, what follows one and a half.
	for b, w := range lower {
		if w != "but" {
			continue
		}
		for i := range vals {
			switch {
			case i < b:
				vals[i] *= 0.5
			case i > b:
				vals[i] *= 1.5
			}
		}
		break
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	emph := punctuationEmphasis(text)
	if sum > 0 {
		sum += emph
	} else if sum < 0 {
		sum -= emph
	}
	return round4(normalize(sum))
}

func valence(words, lower []string, i int, capDiff bool) float64 {
	base, ok := lexicon[lower[i]]
	if !ok {
		return 0
	}
	v := base
	n := len(words)
	if lower[i] == "no" && i != n-1 {
		if _, next := lexicon[lower[i+1]]; next {
			v = 0
		}
	}
	if (i > 0 && lower[i-1] == "no") || (i > 1 && lower[i-2] == "no") ||
		(i > 2 && lower[i-3] == "no" && (lower[i-1] == "or" || lower[i-1] == "nor")) {
		v = base * nScalar
	}
	if isUpper(words[i]) && capDiff {
		if v > 0 {
			v += cIncr
		} else {
			v -= cIncr
		}
	}
	for s := 0; s < 3; s++ {
		if i <= s {
			break
		}
		prev := lower[i-(s+1)]
		if _, inLex := lexicon[prev]; inLex {
			continue
		}
		sc := scalarIncDec(words[i-(s+1)], prev, v, capDiff)
		if s == 1 && sc != 0 {
			sc *= 0.95
		}
		if s == 2 && sc != 0 {
			sc *= 0.9
		}
		v += sc
		v = negationCheck(v, lower, s, i)
		if s == 2 {
			v = idiomsCheck(v, lower, i)
		}
	}
	// "least": "the least helpful" negates, "at least" does not.
	if i > 1 && lower[i-1] == "least" && !inLexicon(lower[i-1]) {
		if lower[i-2] != "at" && lower[i-2] != "very" {
			v *= nScalar
		}
	} else if i > 0 && lower[i-1] == "least" && !inLexicon(lower[i-1]) {
		v *= nScalar
	}
	return v
}

func inLexicon(w string) bool { _, ok := lexicon[w]; return ok }

func scalarIncDec(word, lower string, v float64, capDiff bool) float64 {
	sc, ok := boosters[lower]
	if !ok {
		return 0
	}
	if v < 0 {
		sc = -sc
	}
	if isUpper(word) && capDiff {
		if v > 0 {
			sc += cIncr
		} else {
			sc -= cIncr
		}
	}
	return sc
}

func negated(w string) bool { return negateWords[w] || strings.Contains(w, "n't") }

func negationCheck(v float64, lower []string, s, i int) float64 {
	switch s {
	case 0:
		if negated(lower[i-1]) {
			v *= nScalar
		}
	case 1:
		switch {
		case lower[i-2] == "never" && (lower[i-1] == "so" || lower[i-1] == "this"):
			v *= 1.25
		case lower[i-2] == "without" && lower[i-1] == "doubt":
		case negated(lower[i-2]):
			v *= nScalar
		}
	case 2:
		switch {
		case lower[i-3] == "never" && (lower[i-2] == "so" || lower[i-2] == "this") || (lower[i-1] == "so" || lower[i-1] == "this"):
			v *= 1.25
		case lower[i-3] == "without" && (lower[i-2] == "doubt" || lower[i-1] == "doubt"):
		case negated(lower[i-3]):
			v *= nScalar
		}
	}
	return v
}

func idiomsCheck(v float64, lower []string, i int) float64 {
	seqs := []string{
		lower[i-1] + " " + lower[i],
		lower[i-2] + " " + lower[i-1] + " " + lower[i],
		lower[i-2] + " " + lower[i-1],
		lower[i-3] + " " + lower[i-2] + " " + lower[i-1],
		lower[i-3] + " " + lower[i-2],
	}
	for _, s := range seqs {
		if sc, ok := specialCases[s]; ok {
			v = sc
			break
		}
	}
	if len(lower)-1 > i {
		if sc, ok := specialCases[lower[i]+" "+lower[i+1]]; ok {
			v = sc
		}
	}
	if len(lower)-1 > i+1 {
		if sc, ok := specialCases[lower[i]+" "+lower[i+1]+" "+lower[i+2]]; ok {
			v = sc
		}
	}
	for _, g := range []string{seqs[3], seqs[4], seqs[2]} {
		if b, ok := boosters[g]; ok {
			v += b
		}
	}
	return v
}

func punctuationEmphasis(text string) float64 {
	ep := min(strings.Count(text, "!"), 4)
	amp := float64(ep) * 0.292
	if qm := strings.Count(text, "?"); qm > 1 {
		if qm <= 3 {
			amp += float64(qm) * 0.18
		} else {
			amp += 0.96
		}
	}
	return amp
}

func normalize(score float64) float64 {
	n := score / math.Sqrt(score*score+15)
	return max(-1, min(1, n))
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

const asciiPunct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// sentiWords splits on spaces and strips the punctuation around a word,
// unless that leaves two characters or fewer (an emoticon such as ":)").
func sentiWords(text string) []string {
	var out []string
	for _, w := range strings.Fields(text) {
		s := strings.Trim(w, asciiPunct)
		if len([]rune(s)) <= 2 {
			s = w
		}
		out = append(out, s)
	}
	return out
}

// isUpper is Python's str.isupper: some cased letters, none lower case.
func isUpper(s string) bool {
	cased := false
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			cased = true
		}
	}
	return cased
}

func allCapDifferential(words []string) bool {
	caps := 0
	for _, w := range words {
		if isUpper(w) {
			caps++
		}
	}
	d := len(words) - caps
	return d > 0 && d < len(words)
}

// Label names a compound score: positive, negative or neutral.
func Label(score float64) string {
	switch {
	case score >= 0.05:
		return "positive"
	case score <= -0.05:
		return "negative"
	}
	return "neutral"
}
