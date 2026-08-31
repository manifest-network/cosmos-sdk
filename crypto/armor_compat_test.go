package crypto

import (
	"bytes"
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
		{
			name:      "deterministic multi-header order",
			blockType: "TENDERMINT PRIVATE KEY",
			headers: map[string]string{
				"type": "secp256k1",
				"salt": "00112233445566778899AABBCCDDEEFF",
				"kdf":  "argon2",
			},
			data: bytes.Repeat([]byte{0x5a}, 129),
			want: "-----BEGIN TENDERMINT PRIVATE KEY-----\nkdf: argon2\nsalt: 00112233445566778899AABBCCDDEEFF\ntype: secp256k1\n\nWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\nWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\nWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\n=Omzo\n-----END TENDERMINT PRIVATE KEY-----",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, EncodeArmor(tc.blockType, tc.headers, tc.data))
		})
	}
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

func TestDecodeArmorIgnoresLegacyCRC(t *testing.T) {
	t.Parallel()

	// RFC 9580 section 6.1 requires decoders to ignore a missing, malformed,
	// or mismatched CRC24 footer because it provides no meaningful integrity.
	const mismatchedCRC = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\ncmVwcmVzZW50YXRpdmUgcHVibGljIGtleSBieXRlcw==\n=AAAA\n-----END TENDERMINT PUBLIC KEY-----"

	blockType, headers, data, err := DecodeArmor(mismatchedCRC)
	require.NoError(t, err)
	require.Equal(t, "TENDERMINT PUBLIC KEY", blockType)
	require.Equal(t, map[string]string{"version": "0.0.1"}, headers)
	require.Equal(t, []byte("representative public key bytes"), data)
}

func TestDecodeArmorRejectsMalformedBase64(t *testing.T) {
	t.Parallel()

	const malformed = "-----BEGIN TENDERMINT PUBLIC KEY-----\nversion: 0.0.1\n\n%%%\n-----END TENDERMINT PUBLIC KEY-----"

	_, _, _, err := DecodeArmor(malformed)
	require.Error(t, err)
}
