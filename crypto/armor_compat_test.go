package crypto

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArmorEncodingMatchesLegacyGoldens(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		blockType string
		headers   map[string]string
		data      []byte
		want      string
	}{
		{
			name:      "empty",
			blockType: "TENDERMINT KEY INFO",
			want:      "-----BEGIN TENDERMINT KEY INFO-----\n\n\n=twTO\n-----END TENDERMINT KEY INFO-----",
		},
		{
			name:      "public key metadata",
			blockType: "TENDERMINT PUBLIC KEY",
			headers:   map[string]string{"version": "0.0.1"},
			data:      []byte("representative public key bytes"),
			want:      "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\ncmVwcmVzZW50YXRpdmUgcHVibGljIGtleSBieXRlcw==\n=EoPN\n-----END TENDERMINT PUBLIC KEY-----",
		},
		{
			name:      "wrapped payload",
			blockType: "TENDERMINT PRIVATE KEY",
			headers:   map[string]string{"kdf": "argon2"},
			data:      bytes.Repeat([]byte{0xab}, 129),
			want:      "-----BEGIN TENDERMINT PRIVATE KEY-----\nkdf: argon2\n\nq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6ur\nq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6ur\nq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6ur\n=CK9v\n-----END TENDERMINT PRIVATE KEY-----",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, EncodeArmor(tc.blockType, tc.headers, tc.data))
		})
	}
}

func TestArmorEncodingSortsHeaders(t *testing.T) {
	t.Parallel()

	// The maintained encoder sorts headers; the legacy encoder used map order.
	headers := map[string]string{
		"type": "secp256k1",
		"salt": "00112233445566778899AABBCCDDEEFF",
		"kdf":  "argon2",
	}
	const want = "-----BEGIN TENDERMINT PRIVATE KEY-----\nkdf: argon2\nsalt: 00112233445566778899AABBCCDDEEFF\ntype: secp256k1\n\nWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\nWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\nWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\n=Omzo\n-----END TENDERMINT PRIVATE KEY-----"
	require.Equal(t, want, EncodeArmor("TENDERMINT PRIVATE KEY", headers, bytes.Repeat([]byte{0x5a}, 129)))
}

func TestDecodeArmorAcceptsLegacyHeaderOrder(t *testing.T) {
	t.Parallel()

	const legacy = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\ntype: secp256k1\n\ncmVwcmVzZW50YXRpdmUgcHVibGljIGtleSBieXRlcw==\n=EoPN\n-----END TENDERMINT PUBLIC KEY-----"

	blockType, headers, data, err := DecodeArmor(legacy)
	require.NoError(t, err)
	require.Equal(t, "TENDERMINT PUBLIC KEY", blockType)
	require.Equal(t, map[string]string{"type": "secp256k1", "version": "0.0.1"}, headers)
	require.Equal(t, []byte("representative public key bytes"), data)
}

func TestDecodeArmorChecksLegacyCRC(t *testing.T) {
	t.Parallel()

	const valid = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\ncmVwcmVzZW50YXRpdmUgcHVibGljIGtleSBieXRlcw==\n=EoPN\n-----END TENDERMINT PUBLIC KEY-----"
	withWhitespace := func(s string) string {
		return " \t" + strings.ReplaceAll(s, "\n", " \t\r\n \t") + " \t\r\n"
	}

	testCases := []struct {
		name    string
		armor   string
		wantErr string
	}{
		{name: "valid", armor: valid},
		{name: "missing checksum", armor: strings.Replace(valid, "=EoPN\n", "", 1)},
		{name: "missing checksum and end marker", armor: strings.Split(valid, "\n=EoPN")[0]},
		{name: "whitespace and CRLF", armor: withWhitespace(valid)},
		{name: "mismatched checksum", armor: strings.Replace(valid, "=EoPN", "=AAAA", 1), wantErr: "openpgp: invalid data: armor invalid"},
		{name: "one-byte checksum", armor: strings.Replace(valid, "=EoPN", "=AA==", 1), wantErr: "openpgp: invalid data: armor invalid"},
		{name: "two-byte checksum", armor: strings.Replace(valid, "=EoPN", "=AAA=", 1), wantErr: "openpgp: invalid data: armor invalid"},
		{name: "corrupted payload", armor: strings.Replace(valid, "cmVw", "dmVw", 1), wantErr: "openpgp: invalid data: armor invalid"},
		{name: "mismatched checksum with whitespace", armor: withWhitespace(strings.Replace(valid, "=EoPN", "=AAAA", 1)), wantErr: "openpgp: invalid data: armor invalid"},
		{name: "checksum without end marker", armor: strings.TrimSuffix(valid, "-----END TENDERMINT PUBLIC KEY-----"), wantErr: "openpgp: invalid data: armor invalid"},
		{name: "garbage after checksum", armor: strings.Replace(valid, "=EoPN\n", "=EoPN\ngarbage\n", 1), wantErr: "openpgp: invalid data: armor invalid"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blockType, headers, data, err := DecodeArmor(tc.armor)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				require.Empty(t, blockType)
				require.Nil(t, headers)
				require.Nil(t, data)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "TENDERMINT PUBLIC KEY", blockType)
			require.Equal(t, map[string]string{"version": "0.0.1"}, headers)
			require.Equal(t, []byte("representative public key bytes"), data)
		})
	}
}

func TestDecodeArmorChecksSelectedBlockCRC(t *testing.T) {
	t.Parallel()

	const first = "-----BEGIN TENDERMINT KEY INFO-----\n\n\n=twTO\n-----END TENDERMINT KEY INFO-----"
	const second = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\ncmVwcmVzZW50YXRpdmUgcHVibGljIGtleSBieXRlcw==\n=EoPN\n-----END TENDERMINT PUBLIC KEY-----"
	const rejectedCandidate = "-----BEGIN TENDERMINT KEY INFO-----\ninvalid header\n\n=AAAA\n-----END TENDERMINT KEY INFO-----\n"
	longHeader := strings.Repeat("x", 200)
	testCases := []struct {
		name      string
		armor     string
		blockType string
		headers   map[string]string
		data      []byte
		wantErr   bool
	}{
		{
			name: "checksum in preamble", armor: "preamble\n=AAAA\n" + second,
			blockType: "TENDERMINT PUBLIC KEY", headers: map[string]string{"version": "0.0.1"}, data: []byte("representative public key bytes"),
		},
		{
			name: "rejected header candidate", armor: rejectedCandidate + second,
			blockType: "TENDERMINT PUBLIC KEY", headers: map[string]string{"version": "0.0.1"}, data: []byte("representative public key bytes"),
		},
		{
			name: "long garbage line", armor: strings.Repeat("x", 200) + first + "\n" + second,
			blockType: "TENDERMINT PUBLIC KEY", headers: map[string]string{"version": "0.0.1"}, data: []byte("representative public key bytes"),
		},
		{
			name: "overlong begin line is ignored", armor: "-----BEGIN " + strings.Repeat("X", 200) + "-----\n\n=AAAA\n-----END IGNORED-----\n" + second,
			blockType: "TENDERMINT PUBLIC KEY", headers: map[string]string{"version": "0.0.1"}, data: []byte("representative public key bytes"),
		},
		{
			name: "long header continuation", armor: strings.Replace(second, "version: 0.0.1", "version: 0.0.1\ncomment: "+longHeader, 1),
			blockType: "TENDERMINT PUBLIC KEY", headers: map[string]string{"version": "0.0.1", "comment": longHeader}, data: []byte("representative public key bytes"),
		},
		{
			name: "multiple blocks with different checksums", armor: first + "\n" + second,
			blockType: "TENDERMINT KEY INFO", headers: map[string]string{}, data: []byte{},
		},
		{
			name: "later checksum is ignored", armor: first + "\n" + strings.Replace(second, "=EoPN", "=AAAA", 1),
			blockType: "TENDERMINT KEY INFO", headers: map[string]string{}, data: []byte{},
		},
		{
			name: "missing checksum before later block", armor: strings.Replace(first, "=twTO\n", "", 1) + "\n" + second,
			blockType: "TENDERMINT KEY INFO", headers: map[string]string{}, data: []byte{},
		},
		{
			name: "later valid checksum cannot rescue mismatch", armor: strings.Replace(first, "=twTO", "=AAAA", 1) + "\n" + second,
			wantErr: true,
		},
		{
			name: "mismatched checksum after rejected candidate", armor: rejectedCandidate + strings.Replace(second, "=EoPN", "=AAAA", 1),
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blockType, headers, data, err := DecodeArmor(tc.armor)
			if tc.wantErr {
				require.EqualError(t, err, "openpgp: invalid data: armor invalid")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.blockType, blockType)
			require.Equal(t, tc.headers, headers)
			require.Equal(t, tc.data, data)
		})
	}
}

func TestDecodeArmorRejectsMalformedBase64(t *testing.T) {
	t.Parallel()

	const malformed = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\n%%%%\n-----END TENDERMINT PUBLIC KEY-----"

	_, _, _, err := DecodeArmor(malformed)
	var corruptInput base64.CorruptInputError
	require.ErrorAs(t, err, &corruptInput)
}

func TestDecodeArmorRejectsMalformedCRC(t *testing.T) {
	t.Parallel()

	const malformed = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\ncmVwcmVzZW50YXRpdmUgcHVibGljIGtleSBieXRlcw==\n=%%%%\n-----END TENDERMINT PUBLIC KEY-----"

	_, _, _, err := DecodeArmor(malformed)
	var corruptInput base64.CorruptInputError
	require.ErrorAs(t, err, &corruptInput)
}
