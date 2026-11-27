package pinentry

// promptMessage returns description 的 credential GPG 是 asking
// 为.
func promptMessage(kind Kind) string {
	if kind == KindPIN {
		return "Please enter the PIN for your security key:"
	}
	return "Please enter the passphrase for your GPG key:"
}
