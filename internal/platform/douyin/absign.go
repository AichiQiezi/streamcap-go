// Ported from streamget (MIT), https://github.com/ihmily/streamget
// Source: streamget/platforms/douyin/ab_sign.py — the a_bogus signature (RC4 + SM3 + custom base64).
//
// The Python original works on str values whose code points are all < 256 (it builds them with chr()),
// so this port uses []byte for those strings. Inputs that are real text (query string, user agent) are
// ASCII in practice; SM3 hashes their UTF-8 bytes exactly like Python's str.encode('utf-8').

package douyin

import (
	"math"
	"time"
)

// nowMillis is the clock used by abSign (Python: int(time.time() * 1000)); tests replace it.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

func rc4Encrypt(plaintext, key []byte) []byte {
	var s [256]int
	for i := range s {
		s[i] = i
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j + s[i] + int(key[i%len(key)])) % 256
		s[i], s[j] = s[j], s[i]
	}
	out := make([]byte, len(plaintext))
	i, j := 0, 0
	for n, c := range plaintext {
		i = (i + 1) % 256
		j = (j + s[i]) % 256
		s[i], s[j] = s[j], s[i]
		t := (s[i] + s[j]) % 256
		out[n] = byte(s[t]) ^ c
	}
	return out
}

func leftRotate(x uint32, n int) uint32 {
	n %= 32
	return (x << uint(n)) | (x >> uint(32-n))
}

func sm3Tj(j int) uint32 {
	if j < 16 {
		return 2043430169 // 0x79CC4519
	}
	return 2055708042 // 0x7A879D8A
}

func sm3FF(j int, x, y, z uint32) uint32 {
	if j < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (x & z) | (y & z)
}

func sm3GG(j int, x, y, z uint32) uint32 {
	if j < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (^x & z)
}

// sm3 mirrors the Python SM3 class (including its chunking in write).
type sm3 struct {
	reg   [8]uint32
	chunk []byte
	size  int
}

func newSM3() *sm3 {
	s := &sm3{}
	s.reset()
	return s
}

func (s *sm3) reset() {
	s.reg = [8]uint32{
		1937774191, 1226093241, 388252375, 3666478592,
		2842636476, 372324522, 3817729613, 2969243214,
	}
	s.chunk = nil
	s.size = 0
}

func (s *sm3) write(a []byte) {
	s.size += len(a)
	f := 64 - len(s.chunk)
	if len(a) < f {
		s.chunk = append(s.chunk, a...)
		return
	}
	s.chunk = append(s.chunk, a[:f]...)
	for len(s.chunk) >= 64 {
		s.compress(s.chunk)
		if f < len(a) {
			end := f + 64
			if end > len(a) {
				end = len(a)
			}
			s.chunk = append([]byte(nil), a[f:end]...)
		} else {
			s.chunk = nil
		}
		f += 64
	}
}

func (s *sm3) fill() {
	bitLength := uint64(8 * s.size)
	paddingPos := len(s.chunk)
	s.chunk = append(s.chunk, 0x80)
	paddingPos = (paddingPos + 1) % 64
	if 64-paddingPos < 8 {
		paddingPos -= 64
	}
	for paddingPos < 56 {
		s.chunk = append(s.chunk, 0)
		paddingPos++
	}
	high := bitLength / 4294967296
	for i := 0; i < 4; i++ {
		s.chunk = append(s.chunk, byte(high>>(8*(3-i))))
	}
	for i := 0; i < 4; i++ {
		s.chunk = append(s.chunk, byte(bitLength>>(8*(3-i))))
	}
}

func (s *sm3) compress(data []byte) {
	var w [132]uint32
	for t := 0; t < 16; t++ {
		w[t] = uint32(data[4*t])<<24 | uint32(data[4*t+1])<<16 | uint32(data[4*t+2])<<8 | uint32(data[4*t+3])
	}
	for j := 16; j < 68; j++ {
		a := w[j-16] ^ w[j-9] ^ leftRotate(w[j-3], 15)
		a = a ^ leftRotate(a, 15) ^ leftRotate(a, 23)
		w[j] = a ^ leftRotate(w[j-13], 7) ^ w[j-6]
	}
	for j := 0; j < 64; j++ {
		w[j+68] = w[j] ^ w[j+4]
	}
	a, b, c, d, e, f, g, h := s.reg[0], s.reg[1], s.reg[2], s.reg[3], s.reg[4], s.reg[5], s.reg[6], s.reg[7]
	for j := 0; j < 64; j++ {
		ss1 := leftRotate(leftRotate(a, 12)+e+leftRotate(sm3Tj(j), j), 7)
		ss2 := ss1 ^ leftRotate(a, 12)
		tt1 := sm3FF(j, a, b, c) + d + ss2 + w[j+68]
		tt2 := sm3GG(j, e, f, g) + h + ss1 + w[j]
		d = c
		c = leftRotate(b, 9)
		b = a
		a = tt1
		h = g
		g = leftRotate(f, 19)
		f = e
		e = tt2 ^ leftRotate(tt2, 9) ^ leftRotate(tt2, 17)
	}
	s.reg[0] ^= a
	s.reg[1] ^= b
	s.reg[2] ^= c
	s.reg[3] ^= d
	s.reg[4] ^= e
	s.reg[5] ^= f
	s.reg[6] ^= g
	s.reg[7] ^= h
}

// sum resets, hashes data and returns the 32-byte digest (Python: SM3().sum(data)).
func (s *sm3) sum(data []byte) []byte {
	s.reset()
	s.write(data)
	s.fill()
	for f := 0; f < len(s.chunk); f += 64 {
		s.compress(s.chunk[f : f+64])
	}
	out := make([]byte, 0, 32)
	for _, c := range s.reg {
		out = append(out, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
	}
	s.reset()
	return out
}

var encodingTables = map[string]string{
	"s0": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=",
	"s1": "Dkdpgh4ZKsQB80/Mfvw36XI1R25+WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe=",
	"s2": "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe=",
	"s3": "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe",
	"s4": "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe",
}

func getLongInt(round int, s []byte) int {
	round *= 3
	var c1, c2, c3 int
	if round < len(s) {
		c1 = int(s[round])
	}
	if round+1 < len(s) {
		c2 = int(s[round+1])
	}
	if round+2 < len(s) {
		c3 = int(s[round+2])
	}
	return c1<<16 | c2<<8 | c3
}

func resultEncrypt(long []byte, table string) []byte {
	masks := [4]int{16515072, 258048, 4032, 63}
	shifts := [4]int{18, 12, 6, 0}
	enc := encodingTables[table]
	total := int(math.Ceil(float64(len(long)) / 3 * 4))
	out := make([]byte, 0, total)
	round := 0
	li := getLongInt(round, long)
	for i := 0; i < total; i++ {
		if i/4 != round {
			round++
			li = getLongInt(round, long)
		}
		idx := i % 4
		out = append(out, enc[(li&masks[idx])>>shifts[idx]])
	}
	return out
}

func generRandom(randomNum int, option [2]int) []byte {
	b1 := randomNum & 255
	b2 := (randomNum >> 8) & 255
	return []byte{
		byte((b1 & 170) | (option[0] & 85)),
		byte((b1 & 85) | (option[0] & 170)),
		byte((b2 & 170) | (option[1] & 85)),
		byte((b2 & 85) | (option[1] & 170)),
	}
}

// generateRandomStr uses the same fixed "random" values as the Python/JS reference.
func generateRandomStr() []byte {
	vals := [3]float64{0.123456789, 0.987654321, 0.555555555}
	out := make([]byte, 0, 12)
	out = append(out, generRandom(int(vals[0]*10000), [2]int{3, 45})...)
	out = append(out, generRandom(int(vals[1]*10000), [2]int{1, 0})...)
	out = append(out, generRandom(int(vals[2]*10000), [2]int{1, 5})...)
	return out
}

func splitToBytes(n int64) [4]int {
	return [4]int{int((n >> 24) & 255), int((n >> 16) & 255), int((n >> 8) & 255), int(n & 255)}
}

func generateRC4BBStr(urlSearchParams, userAgent, windowEnv string) []byte {
	const suffix = "cus"
	arguments := [3]int64{0, 1, 14}
	s := newSM3()
	startTime := nowMillis()

	urlList := s.sum(s.sum([]byte(urlSearchParams + suffix)))
	cus := s.sum(s.sum([]byte(suffix)))
	uaKey := []byte{0, 1, 14}
	ua := s.sum(resultEncrypt(rc4Encrypt([]byte(userAgent), uaKey), "s3"))

	endTime := startTime + 100
	const (
		b8     = 3
		pageID = 110624
		aid    = 6383
		b18    = 44
	)
	b := map[int]int{}
	b[18] = b18

	st := splitToBytes(startTime)
	b[20], b[21], b[22], b[23] = st[0], st[1], st[2], st[3]
	b[24] = int(startTime/(1<<32)) & 255
	b[25] = int(startTime/(1<<40)) & 255

	a0 := splitToBytes(arguments[0])
	b[26], b[27], b[28], b[29] = a0[0], a0[1], a0[2], a0[3]
	b[30] = int(arguments[1]/256) & 255
	b[31] = int(arguments[1]%256) & 255
	a1 := splitToBytes(arguments[1])
	b[32], b[33] = a1[0], a1[1]
	a2 := splitToBytes(arguments[2])
	b[34], b[35], b[36], b[37] = a2[0], a2[1], a2[2], a2[3]

	b[38] = int(urlList[21])
	b[39] = int(urlList[22])
	b[40] = int(cus[21])
	b[41] = int(cus[22])
	b[42] = int(ua[23])
	b[43] = int(ua[24])

	et := splitToBytes(endTime)
	b[44], b[45], b[46], b[47] = et[0], et[1], et[2], et[3]
	b[48] = b8
	b[49] = int(endTime/(1<<32)) & 255
	b[50] = int(endTime/(1<<40)) & 255

	pg := splitToBytes(pageID)
	b[52], b[53], b[54], b[55] = pg[0], pg[1], pg[2], pg[3]
	b[57] = aid & 255
	b[58] = (aid >> 8) & 255
	b[59] = (aid >> 16) & 255
	b[60] = (aid >> 24) & 255

	envList := []byte(windowEnv)
	b[64] = len(envList)
	b[65] = b[64] & 255
	b[66] = (b[64] >> 8) & 255
	b[69], b[70], b[71] = 0, 0, 0

	b[72] = b[18] ^ b[20] ^ b[26] ^ b[30] ^ b[38] ^ b[40] ^ b[42] ^ b[21] ^ b[27] ^ b[31] ^
		b[35] ^ b[39] ^ b[41] ^ b[43] ^ b[22] ^ b[28] ^ b[32] ^ b[36] ^ b[23] ^ b[29] ^
		b[33] ^ b[37] ^ b[44] ^ b[45] ^ b[46] ^ b[47] ^ b[48] ^ b[49] ^ b[50] ^ b[24] ^
		b[25] ^ b[52] ^ b[53] ^ b[54] ^ b[55] ^ b[57] ^ b[58] ^ b[59] ^ b[60] ^ b[65] ^
		b[66] ^ b[70] ^ b[71]

	order := []int{
		18, 20, 52, 26, 30, 34, 58, 38, 40, 53, 42, 21,
		27, 54, 55, 31, 35, 57, 39, 41, 43, 22, 28, 32,
		60, 36, 23, 29, 33, 37, 44, 45, 59, 46, 47, 48,
		49, 50, 24, 25, 65, 66, 70, 71,
	}
	bb := make([]byte, 0, len(order)+len(envList)+1)
	for _, k := range order {
		bb = append(bb, byte(b[k]))
	}
	bb = append(bb, envList...)
	bb = append(bb, byte(b[72]))
	return rc4Encrypt(bb, []byte{121})
}

// abSign computes the a_bogus query parameter for a query string and user agent.
func abSign(urlSearchParams, userAgent string) string {
	const windowEnv = "1920|1080|1920|1040|0|30|0|0|1872|92|1920|1040|1857|92|1|24|Win32"
	payload := append(generateRandomStr(), generateRC4BBStr(urlSearchParams, userAgent, windowEnv)...)
	return string(resultEncrypt(payload, "s4")) + "="
}
