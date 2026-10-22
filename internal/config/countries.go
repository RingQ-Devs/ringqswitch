package config

import (
	"sort"
	"strings"
)

// Countries and their calling codes, grouped as RingQ's Allowed countries
// page groups them. Codes are the digits after the + (with the area code
// for countries that share +1 or +7). The "Other" group holds satellite
// phones and global networks, a common target of toll fraud.

// Country is one destination callers may be stopped from calling.
type Country struct {
	ISO       string   `json:"iso"` // ISO 3166 alpha-2 (or a name for the networks of "Other")
	Name      string   `json:"name"`
	Continent string   `json:"continent"`
	Codes     []string `json:"codes"`
}

// Display is the calling code as written: +44, +1-268.
func (c Country) Display() string {
	code := c.Codes[0]
	switch {
	case c.ISO == "CA" || c.ISO == "US":
		return "+1"
	case c.ISO == "KZ" || c.ISO == "RU":
		return "+7"
	case code[0] == '1' && len(code) > 1:
		return "+1-" + code[1:]
	}
	return "+" + code
}

// Continents in the order of the page's tabs.
var Continents = []string{"North America", "South America", "Europe", "Asia", "Africa", "Australia", "Other"}

var canadaAreaCodes = "204 226 236 249 250 257 263 289 306 343 354 365 367 368 382 387 403 416 418 428 431 437 438 450 460 468 474 " +
	"506 514 519 548 579 581 584 587 604 613 639 647 672 683 705 709 742 753 778 780 782 807 819 825 867 873 879 902 905 942"

func areaCodes(country, list string) []string {
	var out []string
	for _, a := range strings.Fields(list) {
		out = append(out, country+a)
	}
	return out
}

// CountryList is every country, by continent and name.
var CountryList = func() []Country {
	type row struct{ iso, name, codes string }
	groups := map[string][]row{
		"North America": {
			{"AG", "Antigua and Barbuda", "1268"}, {"AI", "Anguilla", "1264"}, {"AW", "Aruba", "297"}, {"BS", "Bahamas", "1242"},
			{"BB", "Barbados", "1246"}, {"BL", "Saint Barthélemy", "590"}, {"BM", "Bermuda", "1441"}, {"BQ", "Caribbean Netherlands", "5993 5994 5997"},
			{"BZ", "Belize", "501"}, {"CA", "Canada", ""}, {"CR", "Costa Rica", "506"}, {"CU", "Cuba", "53"}, {"CW", "Curaçao", "5999"},
			{"DM", "Dominica", "1767"}, {"DO", "Dominican Republic", "1809 1829 1849"}, {"GD", "Grenada", "1473"}, {"GL", "Greenland", "299"},
			{"GP", "Guadeloupe", "590"}, {"GT", "Guatemala", "502"}, {"HN", "Honduras", "504"}, {"HT", "Haiti", "509"},
			{"JM", "Jamaica", "1876 1658"}, {"KN", "Saint Kitts and Nevis", "1869"}, {"KY", "Cayman Islands", "1345"},
			{"LC", "Saint Lucia", "1758"}, {"MF", "Saint Martin (French part)", "590"}, {"MQ", "Martinique", "596"},
			{"MS", "Montserrat", "1664"}, {"MX", "Mexico", "52"}, {"NI", "Nicaragua", "505"}, {"PA", "Panama", "507"},
			{"PM", "Saint Pierre and Miquelon", "508"}, {"PR", "Puerto Rico", "1787 1939"}, {"SV", "El Salvador", "503"},
			{"SX", "Sint Maarten (Dutch part)", "1721"}, {"TC", "Turks and Caicos Islands", "1649"}, {"TT", "Trinidad and Tobago", "1868"},
			{"US", "United States of America", "1"}, {"VC", "Saint Vincent and the Grenadines", "1784"},
			{"VG", "Virgin Islands (British)", "1284"}, {"VI", "Virgin Islands (U.S.)", "1340"},
		},
		"South America": {
			{"AR", "Argentina", "54"}, {"BO", "Bolivia", "591"}, {"BR", "Brazil", "55"}, {"CL", "Chile", "56"}, {"CO", "Colombia", "57"},
			{"EC", "Ecuador", "593"}, {"FK", "Falkland Islands", "500"}, {"GF", "French Guiana", "594"}, {"GY", "Guyana", "592"},
			{"PE", "Peru", "51"}, {"PY", "Paraguay", "595"}, {"SR", "Suriname", "597"}, {"UY", "Uruguay", "598"}, {"VE", "Venezuela", "58"},
		},
		"Europe": {
			{"AD", "Andorra", "376"}, {"AL", "Albania", "355"}, {"AT", "Austria", "43"}, {"AX", "Åland Islands", "35818"},
			{"BA", "Bosnia and Herzegovina", "387"}, {"BE", "Belgium", "32"}, {"BG", "Bulgaria", "359"}, {"BY", "Belarus", "375"},
			{"CH", "Switzerland", "41"}, {"CY", "Cyprus", "357"}, {"CZ", "Czechia", "420"}, {"DE", "Germany", "49"}, {"DK", "Denmark", "45"},
			{"EE", "Estonia", "372"}, {"ES", "Spain", "34"}, {"FI", "Finland", "358"}, {"FO", "Faroe Islands", "298"}, {"FR", "France", "33"},
			{"GB", "United Kingdom", "44"}, {"GG", "Guernsey", "441481"}, {"GI", "Gibraltar", "350"}, {"GR", "Greece", "30"},
			{"HR", "Croatia", "385"}, {"HU", "Hungary", "36"}, {"IE", "Ireland", "353"}, {"IM", "Isle of Man", "441624"},
			{"IS", "Iceland", "354"}, {"IT", "Italy", "39"}, {"JE", "Jersey", "441534"}, {"LI", "Liechtenstein", "423"},
			{"LT", "Lithuania", "370"}, {"LU", "Luxembourg", "352"}, {"LV", "Latvia", "371"}, {"MC", "Monaco", "377"}, {"MD", "Moldova", "373"},
			{"ME", "Montenegro", "382"}, {"MK", "North Macedonia", "389"}, {"MT", "Malta", "356"}, {"NL", "Netherlands", "31"},
			{"NO", "Norway", "47"}, {"PL", "Poland", "48"}, {"PT", "Portugal", "351"}, {"RO", "Romania", "40"}, {"RS", "Serbia", "381"},
			{"RU", "Russia", "7"}, {"SE", "Sweden", "46"}, {"SI", "Slovenia", "386"}, {"SJ", "Svalbard and Jan Mayen", "4779"},
			{"SK", "Slovakia", "421"}, {"SM", "San Marino", "378"}, {"UA", "Ukraine", "380"}, {"VA", "Vatican City", "379 3906698"},
			{"XK", "Kosovo", "383"},
		},
		"Asia": {
			{"AE", "United Arab Emirates", "971"}, {"AF", "Afghanistan", "93"}, {"AM", "Armenia", "374"}, {"AZ", "Azerbaijan", "994"},
			{"BD", "Bangladesh", "880"}, {"BH", "Bahrain", "973"}, {"BN", "Brunei", "673"}, {"BT", "Bhutan", "975"}, {"CN", "China", "86"},
			{"GE", "Georgia", "995"}, {"HK", "Hong Kong", "852"}, {"ID", "Indonesia", "62"}, {"IL", "Israel", "972"}, {"IN", "India", "91"},
			{"IO", "British Indian Ocean Territory", "246"}, {"IQ", "Iraq", "964"}, {"IR", "Iran", "98"}, {"JO", "Jordan", "962"},
			{"JP", "Japan", "81"}, {"KG", "Kyrgyzstan", "996"}, {"KH", "Cambodia", "855"}, {"KP", "North Korea", "850"},
			{"KR", "South Korea", "82"}, {"KW", "Kuwait", "965"}, {"KZ", "Kazakhstan", "76 77"}, {"LA", "Laos", "856"},
			{"LB", "Lebanon", "961"}, {"LK", "Sri Lanka", "94"}, {"MM", "Myanmar", "95"}, {"MN", "Mongolia", "976"}, {"MO", "Macau", "853"},
			{"MV", "Maldives", "960"}, {"MY", "Malaysia", "60"}, {"NP", "Nepal", "977"}, {"OM", "Oman", "968"}, {"PH", "Philippines", "63"},
			{"PK", "Pakistan", "92"}, {"PS", "Palestine", "970"}, {"QA", "Qatar", "974"}, {"SA", "Saudi Arabia", "966"},
			{"SG", "Singapore", "65"}, {"SY", "Syria", "963"}, {"TH", "Thailand", "66"}, {"TJ", "Tajikistan", "992"},
			{"TL", "Timor-Leste", "670"}, {"TM", "Turkmenistan", "993"}, {"TR", "Turkey", "90"}, {"TW", "Taiwan", "886"},
			{"UZ", "Uzbekistan", "998"}, {"VN", "Vietnam", "84"}, {"YE", "Yemen", "967"},
		},
		"Africa": {
			{"AO", "Angola", "244"}, {"BF", "Burkina Faso", "226"}, {"BI", "Burundi", "257"}, {"BJ", "Benin", "229"}, {"BW", "Botswana", "267"},
			{"CD", "DR Congo", "243"}, {"CF", "Central African Republic", "236"}, {"CG", "Congo", "242"}, {"CI", "Côte d'Ivoire", "225"},
			{"CM", "Cameroon", "237"}, {"CV", "Cabo Verde", "238"}, {"DJ", "Djibouti", "253"}, {"DZ", "Algeria", "213"}, {"EG", "Egypt", "20"},
			{"ER", "Eritrea", "291"}, {"ET", "Ethiopia", "251"}, {"GA", "Gabon", "241"}, {"GH", "Ghana", "233"}, {"GM", "Gambia", "220"},
			{"GN", "Guinea", "224"}, {"GQ", "Equatorial Guinea", "240"}, {"GW", "Guinea-Bissau", "245"}, {"KE", "Kenya", "254"},
			{"KM", "Comoros", "269"}, {"LR", "Liberia", "231"}, {"LS", "Lesotho", "266"}, {"LY", "Libya", "218"}, {"MA", "Morocco", "212"},
			{"MG", "Madagascar", "261"}, {"ML", "Mali", "223"}, {"MR", "Mauritania", "222"}, {"MU", "Mauritius", "230"}, {"MW", "Malawi", "265"},
			{"MZ", "Mozambique", "258"}, {"NA", "Namibia", "264"}, {"NE", "Niger", "227"}, {"NG", "Nigeria", "234"}, {"RE", "Réunion", "262"},
			{"RW", "Rwanda", "250"}, {"SC", "Seychelles", "248"}, {"SD", "Sudan", "249"}, {"SH", "Saint Helena", "290"},
			{"SL", "Sierra Leone", "232"}, {"SN", "Senegal", "221"}, {"SO", "Somalia", "252"}, {"SS", "South Sudan", "211"},
			{"ST", "São Tomé and Príncipe", "239"}, {"SZ", "Eswatini", "268"}, {"TD", "Chad", "235"}, {"TG", "Togo", "228"},
			{"TN", "Tunisia", "216"}, {"TZ", "Tanzania", "255"}, {"UG", "Uganda", "256"}, {"YT", "Mayotte", "262269 262639"},
			{"ZA", "South Africa", "27"}, {"ZM", "Zambia", "260"}, {"ZW", "Zimbabwe", "263"},
		},
		"Australia": {
			{"AS", "American Samoa", "1684"}, {"AU", "Australia", "61"}, {"CC", "Cocos (Keeling) Islands", "6189162"},
			{"CK", "Cook Islands", "682"}, {"CX", "Christmas Island", "6189164"}, {"FJ", "Fiji", "679"}, {"FM", "Micronesia", "691"},
			{"GU", "Guam", "1671"}, {"KI", "Kiribati", "686"}, {"MH", "Marshall Islands", "692"}, {"MP", "Northern Mariana Islands", "1670"},
			{"NC", "New Caledonia", "687"}, {"NF", "Norfolk Island", "6723"}, {"NR", "Nauru", "674"}, {"NU", "Niue", "683"},
			{"NZ", "New Zealand", "64"}, {"PF", "French Polynesia", "689"}, {"PG", "Papua New Guinea", "675"}, {"PW", "Palau", "680"},
			{"SB", "Solomon Islands", "677"}, {"TK", "Tokelau", "690"}, {"TO", "Tonga", "676"}, {"TV", "Tuvalu", "688"},
			{"VU", "Vanuatu", "678"}, {"WF", "Wallis and Futuna", "681"}, {"WS", "Samoa", "685"},
		},
		"Other": {
			{"INMARSAT", "Inmarsat (satellite)", "870"}, {"GMSS", "Satellite phones (Iridium, Thuraya...)", "881"},
			{"INTL882", "International networks (+882)", "882"}, {"INTL883", "International networks (+883)", "883"},
			{"IPRS", "International premium rate", "979"},
		},
	}
	var out []Country
	for _, cont := range Continents {
		rows := groups[cont]
		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		for _, r := range rows {
			c := Country{ISO: r.iso, Name: r.name, Continent: cont, Codes: strings.Fields(r.codes)}
			if r.iso == "CA" {
				c.Codes = areaCodes("1", canadaAreaCodes)
			}
			out = append(out, c)
		}
	}
	return out
}()

var countryByPrefix = func() map[string]string {
	m := map[string]string{}
	for _, c := range CountryList {
		for _, p := range c.Codes {
			if _, dup := m[p]; !dup || c.ISO == "GP" { // +590 is Guadeloupe first
				m[p] = c.ISO
			}
		}
	}
	return m
}()

// CountryByISO finds a country by its code.
func CountryByISO(iso string) *Country {
	for i := range CountryList {
		if strings.EqualFold(CountryList[i].ISO, iso) {
			return &CountryList[i]
		}
	}
	return nil
}

// InternationalDigits returns the digits after the + of a number written
// internationally (+..., 00..., or 011... as dialled in North America);
// ok is false for a national number.
func InternationalDigits(number string) (string, bool) {
	var b strings.Builder
	for i, r := range number {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	n := b.String()
	switch {
	case strings.HasPrefix(n, "+"):
		n = n[1:]
	case strings.HasPrefix(n, "00"):
		n = n[2:]
	case strings.HasPrefix(n, "011") && len(n) > 10:
		n = n[3:]
	default:
		return "", false
	}
	return n, len(n) >= 3
}

// CountryOfNumber returns the country an international number calls ("" for
// national numbers and unknown codes).
func CountryOfNumber(number string) string {
	d, ok := InternationalDigits(number)
	if !ok {
		return ""
	}
	for n := min(len(d), 7); n > 0; n-- {
		if iso, ok := countryByPrefix[d[:n]]; ok {
			return iso
		}
	}
	return ""
}
