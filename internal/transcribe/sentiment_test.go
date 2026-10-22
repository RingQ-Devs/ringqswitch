package transcribe

import "testing"

// The reference scores come from vaderSentiment 3.3.2 (Python).
func TestSentimentMatchesVADER(t *testing.T) {
	for text, want := range map[string]float64{
		"VADER is smart, handsome, and funny.":                                                0.8316,
		"VADER is smart, handsome, and funny!":                                                0.8439,
		"VADER is very smart, handsome, and funny.":                                           0.8545,
		"VADER is VERY SMART, handsome, and FUNNY.":                                           0.9227,
		"VADER is VERY SMART, handsome, and FUNNY!!!":                                         0.9342,
		"The book was good.":                                                                  0.4404,
		"At least it isn't a horrible book.":                                                  0.431,
		"The book was only kind of good.":                                                     0.3832,
		"The plot was good, but the characters are uncompelling and the dialog is not great.": -0.7042,
		"Today SUX!": -0.5461,
		"Today only kinda sux! But I'll get by, lol":  0.5249,
		"Make sure you :) or :D today!":               0.8633,
		"Not bad at all":                              0.431,
		"Thank you so much, that was really helpful.": 0.6801,
		"This is ridiculous, I have been waiting for an hour and nobody called me back!": -0.4199,
		"I am not happy with the service.":                                               -0.4585,
		"Okay, let me check that for you.":                                               0.2263,
		"That is the least helpful answer I have ever heard.":                            -0.3252,
		"It was never so good.":                                                          0.6626,
		"No problem at all, happy to help.":                                              0.8252,
		"Without doubt the best support.":                                                0.8406,
		"I'm extremely disappointed and frustrated.":                                     -0.7939,
		"Can you please hold?":                                                           0.3182,
		"What?? Why??":                                                                   0,
		"I hate this, it's the worst experience ever":                                    -0.8316,
	} {
		if got := Sentiment(text); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

func TestSentimentCallWords(t *testing.T) {
	if s := Sentiment("I was overcharged on my bill"); Label(s) != "negative" {
		t.Errorf("overcharged: %v", s)
	}
	if Label(0.04) != "neutral" || Label(0.05) != "positive" || Label(-0.05) != "negative" {
		t.Error("labels")
	}
}
