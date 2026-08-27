package crypt

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rcloneVectorPair struct{ plain, enc string }

var rcloneCompatVectors = []struct {
	label, password, salt, encoding, mode string
	pairs                                 []rcloneVectorPair
}{
	{"standard/base32", "testpassword", "testsalt", "base32", "standard", []rcloneVectorPair{
		{"\u7535\u5f71.mp4", "i6gmhg960jc2j91t7a3r7i37j4"},
		{"file-with-dashes_underscores.js", "d9dhjckon9nps9e0gadfgdsmtrk4mkarpdb7ga78jbd78o18peg0"},
		{"file with spaces and UPPER case.TXT", "475lh1813d0rv2i9srs17gk5hpqkn6esr8os4hs9iuhqipirngtajhubitb9ugih2kfbvdekik25k"},
		{"h\u00e9llo w\u00f6rld \u00fcn\u00efcode", "c275q9c25rilkdieahmpovenf6bnh185di8718k20oj627dkri4g"},
		{"star*name??.txt", "a6rq8lvokpc92fcqn9dc6bt6rc"},
		{"hello world.txt", "7ajmr7dan4k7068t0bqchvoqhc"},
		{"\u6d4b\u8bd5\u4e2d\u6587\u6587\u4ef6\u540d.md", "artaq232v205gokeiuh050d6a5lvucujhd2soro0kinhglcahg9g"},
		{"trailing space ", "i166sn82erf99p747htptnr4io"},
		{"this_is_a_reasonably_long_filename_under_one_hundred_and_twenty_bytes_so_the_encrypted_name_still_fits_under_the_macos_255_byte_limit.txt", "btp7upr6eccbial0b5n9dd777j3km3ucuau5i7jmonkeopdh8vaa9mp2kuabcvr9ejai8c2ag4ev7k0v22pnqogdkth8v2jf1thcdhet0v2fm68qgi049adid2gkgupclncm94489omep58vqf7fec2v49i35fffvr9bbhrf001d9rmfn0i9d54du0d63osvgdcnc18ienr9sj5nes21a2tt11tjtjin53ae4a0"},
		{"\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt", "p09fp40hr84sah4fek44jsfj33erctssk50gqt5kau9qpiebv8u0"},
		{"\u0437\u0430\u0433\u043e\u043b\u043e\u0432\u043e\u043a.txt", "o4ftft52rbkoe4pobehl545q9fqto4b1rhus4n3rlkfqekje37n0"},
		{"quote'apostrophe.txt", "4shvk15tdfqmb3ocvmilmciusva0b4h7huprtaeri5qd79emf6vg"},
		{"README.md", "b5rfha73ip7ln3gntta1f2gko0"},
		{"parens(name).md", "8ubcv69oko9de5ca7onla6n3ig"},
		{".hidden", "e5mas2hr43tqr0hhfcjsjejdo0"},
		{"semi;colon.txt", "h65afhifm111nmkr7fsirv6pbc"},
		{"emoji 🚀 test.png", "acjf2ja8kk6qqg1e0ind62beugcia8do58rokrdfej8lab2hskj0"},
		{"a", "24107mvr780pg3vvitbrhqs7lk"},
		{"1234567890", "2qtpdv2jve9a9jthsltc4l80jg"}}},
	{"standard/base64", "testpassword", "testsalt", "base64", "standard", []rcloneVectorPair{
		{"a", "EQID2_s6AZgP_5dXuOuHrQ"},
		{"file with spaces and UPPER case.TXT", "IctYhQEbQb-KSeb4E8KFjnVLmdzaMcJHiZejqWZbvDqpx8uXVp9CURUev7XUlQRa"},
		{"quote'apostrophe.txt", "JyP6BL1r9WWPDP2lWzJe59QFkiePs76p25F006XWeb8"},
		{"hello world.txt", "Oqdtnaq5KHAZHQL0yP8aiw"},
		{"parens(name).md", "R5bPmTimEtcVij4vVRrjlA"},
		{"README.md", "WXb4qOOWT1uOF-9UF4oUwA"},
		{"\u6d4b\u8bd5\u4e2d\u6587\u6587\u4ef6\u540d.md", "VvqtCGL4gFhijpeiAoGmUWv_M9OLRcxvAKSvGFWKjBM"},
		{"semi;colon.txt", "iYqnxk-wQhvamzv5LfzZWw"},
		{"\u7535\u5f71.mp4", "kaFowSYE2CmkPTqHs8hnmQ"},
		{".hidden", "cWyuCjsg-62CMXsnybptwA"},
		{"h\u00e9llo w\u00f6rld \u00fcn\u00efcode", "YI5dJYIu5Vo2TlRtnH3XeZd4hQVskHCiggYmYR203Ik"},
		{"\u0437\u0430\u0433\u043e\u043b\u043e\u0432\u043e\u043a.txt", "wR_X9KLa6YcTOFujUpC6S_XcEWHcfcJce60fp1JuGe4"},
		{"1234567890", "FruW_FP7kqTPseV6wlUAnA"},
		{"emoji 🚀 test.png", "UybxTUilDa1ALgSu0wlu9BklIbgqN4ptr3TRVSxR5SY"},
		{"trailing space ", "kExuXQJ23pTk5Dx7nt9klg"},
		{"this_is_a_reasonably_long_filename_under_one_hundred_and_twenty_bytes_so_the_encrypted_name_still_fits_under_the_macos_255_byte_limit.txt", "X3J_Z2ZzGLkqoFlulrTnPMdLD8zyvFkedsXo7GWxR9Sk2yKnlLZ_aXTVJDBKgR3z0B8Qs31iDadij4pvD2LGxd0HxPsZGoSARKmyaKFIeyyt2WSQiE4s7JUf0873MF8iZDK97_7StcdvAALU7s-4JJaUjfAaYeOfg1l2BRJ19p5Mt3cEFQu9CHs-zlco1OIo"},
		{"file-with-dashes_underscores.js", "alsZspi6b54lwIKa-DeW7uhLUVvLVngo6JradGAoy6A"},
		{"star*name??.txt", "UbekV_imWJE9mrpawy-m2w"},
		{"\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt", "yBL8kBHaCcVEj3UISfHzGN22d5yhQQ10tFeTrMnL-jw"}}},
	{"obfuscate/base32", "testpassword", "testsalt", "base32", "obfuscate", []rcloneVectorPair{
		{"\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt", "66.\u6542\u6789\u8afb\u3032\u30fe\u3001\u3048.EIE"},
		{"h\u00e9llo w\u00f6rld \u00fcn\u00efcode", "123.z\u00ecDDG O\u00f9JDv \u00ffF\u00f2uGvw"},
		{"file-with-dashes_underscores.js", "69.twzs-KwHv-roGvsG_IBrsFGqCFsG.xG"},
		{"1234567890", "13.3456789012"},
		{"quote'apostrophe.txt", "40.AEyDo'kzyCDByzro.DHD"},
		{"this_is_a_reasonably_long_filename_under_one_hundred_and_twenty_bytes_so_the_encrypted_name_still_fits_under_the_macos_255_byte_limit.txt", "205.SGHR_HR_z_QDzRNMzAKX_KNMF_EHKDMzLD_TMCDQ_NMD_GTMCQDC_zMC_SVDMSX_AXSDR_RN_SGD_DMBQXOSDC_MzLD_RSHKK_EHSR_TMCDQ_SGD_LzBNR_700_AXSD_KHLHS.SWS"},
		{"file with spaces and UPPER case.TXT", "4.DGJC UGRF QNyACQ yLB snncp AyQC.rvr"},
		{"README.md", "173.jWSVeW.Ev"},
		{".hidden", "154..FGBBCL"},
		{"trailing space ", "166.ECltwtyr DAlnp "},
		{"semi;colon.txt", "146.IuCy;sEBED.JNJ"},
		{"parens(name).md", "122.GrIvEJ(ErDv).Du"},
		{"hello world.txt", "234.lipps Asvph.xBx"},
		{"\u0437\u0430\u0433\u043e\u043b\u043e\u0432\u043e\u043a.txt", "137.\u045c\u0455\u0458\u0463\u0460\u0463\u0457\u0463\u045f.AEA"},
		{"star*name??.txt", "145.HIpG*CpBt??.IMI"},
		{"\u7535\u5f71.mp4", "229.\u7537\u5f73.KN6"},
		{"emoji 🚀 test.png", "7.goqlk 🚢 vguv.rpi"},
		{"a", "97.r"},
		{"\u6d4b\u8bd5\u4e2d\u6587\u6587\u4ef6\u540d.md", "93.\u6dc3\u8b4d\u4ea5\u65ff\u65ff\u4e6e\u5485.zq"}}},
	{"second password/base32", "second_pass word", "second salt \u76d0", "base32", "standard", []rcloneVectorPair{
		{"h\u00e9llo w\u00f6rld \u00fcn\u00efcode", "djhjtahmlvn9mtqetp5cqhkm403qcacchi2gddauktrjmvl55tsg"},
		{"file-with-dashes_underscores.js", "23b2fi95mkm5amd2k7qkstrlti41asr4p0j2n4785rhjskqe0eu0"},
		{"\u7535\u5f71.mp4", "egh95kvspan4drhkt4ibcml9cs"},
		{"semi;colon.txt", "nt4ml8u3l3hrqtffe7o3k6ld3c"},
		{"emoji 🚀 test.png", "7b1sm48fikp2a79bvsl6e9h45g52fnlt4m77vggt3rqffpm93gd0"},
		{".hidden", "i8tg9kaad1t8vngbe6jfnka588"},
		{"parens(name).md", "sk9d4uko4t9fnokgntmgrja368"},
		{"\u6d4b\u8bd5\u4e2d\u6587\u6587\u4ef6\u540d.md", "p5e1n6uqb0hflhai6oaaa0igobp3qfa74e0hv5r9f786h2t01lj0"},
		{"this_is_a_reasonably_long_filename_under_one_hundred_and_twenty_bytes_so_the_encrypted_name_still_fits_under_the_macos_255_byte_limit.txt", "mkj1e5ugqrhje09tk595hudpbdevgb4pjno7a7mltrn7hsnp7569e0n9nhd7pod28636qqutgos44c8tpb2olep8bq22jpb7qrurm8bhi5k38g3ut88agg4ots2mnbioln73ehu2flcoun0v91kt0iq5d55ncfkr2lo85oag2gpdpqjdguo156ftjumes3k5ldsmhio0atru6c6f8dppvdnv9ah2hn0biht87ro"},
		{"a", "t593tommuvu909hu1dcb6ovgj4"},
		{"README.md", "ho88kqorkarmebkdcvg064geo4"},
		{"\u0437\u0430\u0433\u043e\u043b\u043e\u0432\u043e\u043a.txt", "3gq9oeob3eg9ogv65berhadoro8p1sqjkjuqt3fph0hi4mmsj1p0"},
		{"file with spaces and UPPER case.TXT", "a0pq9l8q9mtu4pdq5j83f92gth7qng5gbs970dchpc8nmnpvqcprok650b7mcmc2rgn223mocj4ee"},
		{"\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt", "nd1q5ka66hdf42eonmd4gh9qo25m00vjio5jrbb8onse84utnj70"},
		{"hello world.txt", "sdlh4mv74dgavs870hpnr2ipv4"},
		{"trailing space ", "d6f0p5n46c3r1gmfq960tp1llk"},
		{"1234567890", "t5v986ubf622oh56qt5o9pnl54"},
		{"quote'apostrophe.txt", "eueefj7avprrj1o4fuih29stnbsige3fqgngu5mgnoq1a5odq47g"},
		{"star*name??.txt", "fdfnc7ms3jiqemf0iovkjabcss"}}},
}

// defaultSalt: same password, no salt (rclone falls back to the fixed
// defaultSalt exactly like this implementation).
var rcloneCompatDefaultSaltVectors = []rcloneVectorPair{
	{"parens(name).md", "a0j8uj0sn0ne6qr427ldmrco00"},
	{"\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt", "k8pioqbqg8pf6q9elhftnukik17jcocgs2u1elm0utb00hm4r780"},
	{"h\u00e9llo w\u00f6rld \u00fcn\u00efcode", "e2p7e1hf268d2dja7of97tackpq9c0ji8kdtfcn27oaq4903rpmg"},
	{"\u7535\u5f71.mp4", "95coti43ujo36ehmcsvu849fc0"},
	{".hidden", "5fg5mukttbn8feljf0qunud3cs"},
	{"file with spaces and UPPER case.TXT", "j615ku2l4krqlsorl1ln4055f6j42oc600q27roh39lancln8r5ssatiit8uifju4lvlcql7irnd6"},
	{"emoji 🚀 test.png", "5sgbh0b854ptp4qtcedj97tp9p469a136tnv1mfqhpe54jpcs80g"},
	{"this_is_a_reasonably_long_filename_under_one_hundred_and_twenty_bytes_so_the_encrypted_name_still_fits_under_the_macos_255_byte_limit.txt", "6h13dc2v4b8nk1rd3tcrigg8mhl1uklg4b2i8bfibpgcueu6f337hv45cmviu5cka43hmbk0ebc0hl42kv76ohu70933fhcarv9o8k31g9t2r77bfhe525aab40q7kp1nuovj3ne0s795ph3hsasu73h81ndo4mts6dubstl21o17p45hlbv1242mr3v6ietea0tc6nmqa6v5ct9e19261jprvus5qlja42csr0"},
	{"file-with-dashes_underscores.js", "1a8v7v58f8lhqsj3897m9gd69haa53ci6ho86555ojcv39er6q6g"},
	{"trailing space ", "hcrbmncvbjh1ljepp0urjv06dc"},
	{"hello world.txt", "vpsc0nuo7egcjm9bbsq1fq2puo"},
	{"\u6d4b\u8bd5\u4e2d\u6587\u6587\u4ef6\u540d.md", "jk50tnsnifbp25h2nb3iqchq273iotglmbbbo9pirqblj22c4ufg"},
	{"\u0437\u0430\u0433\u043e\u043b\u043e\u0432\u043e\u043a.txt", "p3no0kgjomfml42goah564ei95vbvee7p3j36jbgv15vtvnc5ddg"},
	{"README.md", "g2555i8100sc2cmrokto9flbe4"},
	{"semi;colon.txt", "ahunklisimo7sifs903o9tsb44"},
	{"quote'apostrophe.txt", "rie8gmn8fbq9adu3knnjq4vf022j4p2n5ktr01fib3fdlbbep67g"},
	{"a", "l5vi5hrcus3aigujduslfn6tj0"},
	{"star*name??.txt", "3o5a9n62v79u5bpj1gi80vfu20"},
	{"1234567890", "77ro7e0i9mlgi3t41g8n6arirs"},
}

// TestRcloneCompatFilenameVectors pins EncryptSegment/DecryptSegment to golden
// vectors captured from the real rclone binary v1.73.3 (see
// pkg/crypt/testdata/rclone/README.md for the capture recipe). rclone encrypts
// names with AES-EME using the scrypt-derived name key/tweak, then encodes the
// ciphertext with lower-case base32 (hex alphabet, no padding) or base64
// (URL-safe, no padding); the salt is the raw bytes of (obscured) password2.
func TestRcloneCompatFilenameVectors(t *testing.T) {
	for _, tc := range rcloneCompatVectors {
		t.Run(tc.label, func(t *testing.T) {
			c, err := NewRcloneCipher(tc.password, tc.salt, tc.encoding, tc.mode)
			if err != nil {
				t.Fatalf("NewRcloneCipher: %v", err)
			}
			for _, v := range tc.pairs {
				got := c.EncryptSegment(v.plain)
				if got != v.enc {
					t.Errorf("EncryptSegment(%q) = %q, rclone v1.73.3 = %q", v.plain, got, v.enc)
				}
				back, err := c.DecryptSegment(v.enc)
				if err != nil {
					t.Errorf("DecryptSegment(%q): %v", v.enc, err)
					continue
				}
				if back != v.plain {
					t.Errorf("DecryptSegment(%q) = %q, want %q", v.enc, back, v.plain)
				}
			}
		})
	}
}

// TestRcloneCompatDefaultSaltVector pins the no-salt case: rclone v1.73.3 and
// this implementation share the same fixed defaultSalt when no salt is
// configured, so both derive identical keys from the bare password.
func TestRcloneCompatDefaultSaltVector(t *testing.T) {
	c, err := NewRcloneCipher("testpassword", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rcloneCompatDefaultSaltVectors {
		if got := c.EncryptSegment(v.plain); got != v.enc {
			t.Errorf("EncryptSegment(%q) = %q, rclone v1.73.3 = %q", v.plain, got, v.enc)
		}
		if back, err := c.DecryptSegment(v.enc); err != nil || back != v.plain {
			t.Errorf("DecryptSegment(%q) = %q, err=%v, want %q", v.enc, back, err, v.plain)
		}
	}
}

// TestRcloneCompatObfuscateIgnoresEncoding verifies obfuscate mode output is
// independent of the filename encoding, matching rclone (rclone obfuscates the
// segment and only encodes when the mode is "standard").
func TestRcloneCompatObfuscateIgnoresEncoding(t *testing.T) {
	for _, tc := range rcloneCompatVectors {
		if tc.mode != "obfuscate" {
			continue
		}
		c32, _ := NewRcloneCipher(tc.password, tc.salt, "base32", "obfuscate")
		c64, _ := NewRcloneCipher(tc.password, tc.salt, "base64", "obfuscate")
		for _, v := range tc.pairs {
			if a, b := c32.EncryptSegment(v.plain), c64.EncryptSegment(v.plain); a != b {
				t.Errorf("obfuscate output depends on encoding for %q: %q vs %q", v.plain, a, b)
			}
		}
	}
}

// TestRcloneCompatObscureVectors reveals `rclone obscure` outputs captured from
// the real binary. rclone's obscuring is AES-CTR with a random IV, so the
// ciphertexts below are frozen one-shot samples: revealing them must yield the
// original plaintext on every run.
func TestRcloneCompatObscureVectors(t *testing.T) {
	cases := []struct{ obscured, plain string }{
		{"GhQq9modU_NHieFDHYBkqBzvVhHG4WH7ieh6oA", "testpassword"},
		{"oi_4wkOXRyrds3EOlzP3DFUtqvLHedI3", "testsalt"},
		{"64auhKZFmmIs7c6yHPYGCueWnMsd2iqjSKiTJUpHlZY", "second_pass word"},
		{"Fh00mLbTcvge_VH34QaFawQod4jNGTOutRVd8bAOqA", "second salt \u76d0"},
		{"cT0W34ImvMe_nr_m_QDmVUg", "q"},
	}
	for _, tc := range cases {
		got, err := RevealRcloneConfigValue(tc.obscured)
		if err != nil {
			t.Errorf("RevealRcloneConfigValue(%q): %v", tc.obscured, err)
			continue
		}
		if got != tc.plain {
			t.Errorf("RevealRcloneConfigValue(%q) = %q, want %q", tc.obscured, got, tc.plain)
		}
	}
}

// TestRcloneCompatConfigValuesWrittenByRclone reveals the obscured values real
// rclone wrote into its config file for password and password2 (the salt), and
// proves a cipher configured with the plain values and one configured with the
// obscured values agree.
func TestRcloneCompatConfigValuesWrittenByRclone(t *testing.T) {
	obscuredPassword := "fNF2H9xJfORlv6bQxPmXMDKuTDGdhDCcS5bFcQ"
	obscuredSalt := "aTFjB4QoB1VwiXN3wpCnSwU6fMwhaZhF"
	if pw, err := RevealRcloneConfigValue(obscuredPassword); err != nil || pw != "testpassword" {
		t.Errorf("password reveal = %q, err=%v", pw, err)
	}
	if salt, err := RevealRcloneConfigValue(obscuredSalt); err != nil || salt != "testsalt" {
		t.Errorf("salt reveal = %q, err=%v", salt, err)
	}
	fromPlain, err := NewRcloneCipherFromConfig(Config{Password: "testpassword", Salt: "testsalt"})
	if err != nil {
		t.Fatal(err)
	}
	fromObscured, err := NewRcloneCipherFromConfig(Config{
		Password:         obscuredPassword,
		PasswordObscured: true,
		Salt:             obscuredSalt,
		SaltObscured:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "\u7535\u5f71.mp4"} {
		if a, b := fromPlain.EncryptSegment(name), fromObscured.EncryptSegment(name); a != b {
			t.Errorf("cipher mismatch for %q: %q vs %q", name, a, b)
		}
	}
}

// rcloneFixturePlaintext reproduces the deterministic corpus used to generate
// the rclone ciphertext fixtures (testdata/rclone/README.md documents the
// recipe), keyed by plaintext file name.
func rcloneFixturePlaintext(name string) []byte {
	rcloneFixtureSizes := map[string]int{
		"empty.bin":      0,
		"tiny.bin":       100,
		"oneblock.bin":   64 * 1024,
		"oneplus.bin":    64*1024 + 1,
		"multiblock.bin": 200000,
		"big.bin":        1048600,
	}
	size, ok := rcloneFixtureSizes[name]
	if !ok {
		return nil
	}
	row := []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ\n")
	out := make([]byte, size)
	for i := range out {
		out[i] = row[i%len(row)]
	}
	return out
}

// TestRcloneCompatDataFixtures decrypts ciphertext files that the real rclone
// binary v1.73.3 produced for a deterministic plaintext corpus (stored under
// pkg/crypt/testdata/rclone). Each file is a full rclone-encrypted object:
// 32-byte header (magic + random nonce) followed by secretbox blocks. A
// matching DecryptingReader must recover the exact plaintext, and the size
// accounting (EncryptedSize/DecryptedSize) must agree with the stored bytes.
func TestRcloneCompatDataFixtures(t *testing.T) {
	dataRoot := filepath.Join("testdata", "rclone")
	for _, encDir := range []string{"enc32", "enc64"} {
		mapFile := "map32.txt"
		if encDir == "enc64" {
			mapFile = "map64.txt"
		}
		rows, err := os.ReadFile(filepath.Join(dataRoot, mapFile))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(rows)), "\n") {
			// The map file is ASCII text and can come back CRLF-normalised
			// from the checkout; strip the trailing \r so names stay exact.
			enc, plain, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
			if !ok {
				t.Fatalf("bad map line %q", line)
			}
			want[enc] = plain
		}
		cp, err := NewRcloneCipher("testpassword", "testsalt")
		if err != nil {
			t.Fatal(err)
		}
		ents, err := os.ReadDir(filepath.Join(dataRoot, encDir))
		if err != nil {
			t.Fatal(err)
		}
		if len(ents) != len(want) {
			t.Fatalf("%s: found %d ciphertexts, map has %d", encDir, len(ents), len(want))
		}
		for _, ent := range ents {
			encName := ent.Name()
			plainName, ok := want[encName]
			if !ok {
				t.Errorf("%s: %s not in map", encDir, encName)
				continue
			}
			ciphertext, err := os.ReadFile(filepath.Join(dataRoot, encDir, encName))
			if err != nil {
				t.Fatal(err)
			}
			// The expected plaintext is synthesized from the known corpus
			// pattern instead of reading testdata/rclone/plain back: those
			// files are ASCII text and may be CRLF-normalised on Windows
			// checkouts, while binary ciphertext never is.
			plain := rcloneFixturePlaintext(plainName)
			if len(plain) == 0 && plainName != "empty.bin" {
				t.Fatalf("%s: unknown fixture %q", encDir, plainName)
			}
			if string(ciphertext[:FileMagicSize]) != FileMagic {
				t.Errorf("%s/%s: bad magic", encDir, encName)
				continue
			}
			var nonce [FileNonceSize]byte
			copy(nonce[:], ciphertext[FileMagicSize:FileHeaderSize])
			got, err := io.ReadAll(NewDecryptingReader(bytes.NewReader(ciphertext[FileHeaderSize:]), cp, nonce))
			if err != nil {
				t.Errorf("%s/%s: DecryptingReader: %v", encDir, encName, err)
				continue
			}
			if !bytes.Equal(got, plain) {
				t.Errorf("%s/%s: decrypted %d bytes, plain %s has %d", encDir, encName, len(got), plainName, len(plain))
			}
			if want := cp.EncryptedSize(int64(len(plain))); want != int64(len(ciphertext)) {
				t.Errorf("%s/%s: EncryptedSize(%d) = %d, file has %d", encDir, encName, len(plain), want, len(ciphertext))
			}
			if want, err := cp.DecryptedSize(int64(len(ciphertext))); err != nil || want != int64(len(plain)) {
				t.Errorf("%s/%s: DecryptedSize(%d) = %d, err=%v, want %d", encDir, encName, len(ciphertext), want, err, len(plain))
			}
		}
	}
}

// TestRcloneCompatSizeFormula pins the header/block geometry shared with
// rclone: 32-byte header, 16-byte secretbox overhead per block, 64 KiB data
// per block.
func TestRcloneCompatSizeFormula(t *testing.T) {
	c, _ := NewRcloneCipher("p", "")
	cases := []struct {
		plain, enc int64
	}{
		{0, 32},
		{1, 49},
		{100, 148},
		{BlockDataSize, 32 + BlockSize},
		{BlockDataSize + 1, 32 + 2*BlockSize - (BlockDataSize - 1)},
		{2 * BlockDataSize, 32 + 2*BlockSize},
		{200000, 200096},
		{1048600, 1048904},
	}
	for _, tc := range cases {
		if got := c.EncryptedSize(tc.plain); got != tc.enc {
			t.Errorf("EncryptedSize(%d) = %d, want %d", tc.plain, got, tc.enc)
		}
		if got, err := c.DecryptedSize(tc.enc); err != nil || got != tc.plain {
			t.Errorf("DecryptedSize(%d) = %d, err=%v, want %d", tc.enc, got, err, tc.plain)
		}
	}
}

// rcloneCipherTestVectors are ported verbatim from rclone v1.73.3's own
// backend/crypt/cipher_test.go (TestEncryptSegmentBase32 / Base64). rclone
// asserts these known-answer vectors in its upstream CI with password="" and
// no salt (i.e. the shared fixed defaultSalt), which also covers the empty
// password path that the binary-captured tables above do not.
var rcloneCipherTestVectors = []struct {
	encoding string
	pairs    []rcloneVectorPair
}{
	{"base32", []rcloneVectorPair{
		{"", ""},
		{"1", "p0e52nreeaj0a5ea7s64m4j72s"},
		{"12", "l42g6771hnv3an9cgc8cr2n1ng"},
		{"123", "qgm4avr35m5loi1th53ato71v0"},
		{"1234", "8ivr2e9plj3c3esisjpdisikos"},
		{"12345", "rh9vu63q3o29eqmj4bg6gg7s44"},
		{"123456", "bn717l3alepn75b2fb2ejmi4b4"},
		{"1234567", "n6bo9jmb1qe3b1ogtj5qkf19k8"},
		{"12345678", "u9t24j7uaq94dh5q53m3s4t9ok"},
		{"123456789", "37hn305g6j12d1g0kkrl7ekbs4"},
		{"1234567890", "ot8d91eplaglb62k2b1trm2qv0"},
		{"12345678901", "h168vvrgb53qnrtvvmb378qrcs"},
		{"123456789012", "s3hsdf9e29ithrqbjqu01t8q2s"},
		{"1234567890123", "cf3jimlv1q2oc553mv7s3mh3eo"},
		{"12345678901234", "moq0uqdlqrblrc5pa5u5c7hq9g"},
		{"123456789012345", "eeam3li4rnommi3a762h5n7meg"},
		{"1234567890123456", "mijbj0frqf6ms7frcr6bd9h0env53jv96pjaaoirk7forcgpt70g"},
	}},
	{"base64", []rcloneVectorPair{
		{"", ""},
		{"1", "yBxRX25ypgUVyj8MSxJnFw"},
		{"12", "qQUDHOGN_jVdLIMQzYrhvA"},
		{"123", "1CxFf2Mti1xIPYlGruDh-A"},
		{"1234", "RL-xOTmsxsG7kuTy2XJUxw"},
		{"12345", "3FP_GHoeBJdq0yLgaED8IQ"},
		{"123456", "Xc4T1Gqrs3OVYnrE6dpEWQ"},
		{"1234567", "uZeEzssOnDWHEOzLqjwpog"},
		{"12345678", "8noiTP5WkkbEuijsPhOpxQ"},
		{"123456789", "GeNxgLA0wiaGAKU3U7qL4Q"},
		{"1234567890", "x1DUhdmqoVWYVBLD3dha-A"},
		{"12345678901", "iEyP_3BZR6vvv_2WM6NbZw"},
		{"123456789012", "4OPGvS4SZdjvS568APUaFw"},
		{"1234567890123", "Y8c5Wr8OhYYUo7fPwdojdg"},
		{"12345678901234", "tjQPabXW112wuVF8Vh46TA"},
		{"123456789012345", "c5Vh1kTd8WtIajmFEtz2dA"},
		{"1234567890123456", "tKa5gfvTzW4d-2bMtqYgdf5Rz-k2ZqViW6HfjbIZ6cE"},
	}},
}

// TestRcloneCompatUpstreamCipherVectors checks the vectors rclone's own test
// suite asserts, so these stay in lockstep with upstream rclone CI.
func TestRcloneCompatUpstreamCipherVectors(t *testing.T) {
	for _, tc := range rcloneCipherTestVectors {
		c, err := NewRcloneCipher("", "", tc.encoding)
		if err != nil {
			t.Fatalf("NewRcloneCipher: %v", err)
		}
		for _, v := range tc.pairs {
			if got := c.EncryptSegment(v.plain); got != v.enc {
				t.Errorf("[%s] EncryptSegment(%q) = %q, rclone test vector = %q", tc.encoding, v.plain, got, v.enc)
			}
			back, err := c.DecryptSegment(v.enc)
			if err != nil || back != v.plain {
				t.Errorf("[%s] DecryptSegment(%q) = %q, err=%v, want %q", tc.encoding, v.enc, back, err, v.plain)
			}
		}
	}
}
