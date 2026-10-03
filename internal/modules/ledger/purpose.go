package ledger

// Purpose adalah tujuan journal (docs/design/04-domain-model.md §6). Setiap purpose punya
// TEMPLATE: akun jenis apa yang boleh didebit dan dikredit. Template ditulis sebagai DATA,
// bukan if-else tersebar, sehingga bisa diuji sebagai tabel (TestJournalPurpose_Templates).
type Purpose string

const (
	PurposeTopupCredit        Purpose = "TOPUP_CREDIT"
	PurposeTransfer           Purpose = "TRANSFER"
	PurposePayment            Purpose = "PAYMENT"
	PurposeRefund             Purpose = "REFUND"
	PurposePayoutHold         Purpose = "PAYOUT_HOLD"
	PurposePayoutSettle       Purpose = "PAYOUT_SETTLE"
	PurposePayoutReversal     Purpose = "PAYOUT_REVERSAL"
	PurposeProviderSettlement Purpose = "PROVIDER_SETTLEMENT"
	PurposeAdjustmentManual   Purpose = "ADJUSTMENT_MANUAL"
)

// AccountKind adalah jenis akun yang relevan untuk template.
type AccountKind string

const (
	KindUserWallet       AccountKind = "USER_WALLET"
	KindMerchantWallet   AccountKind = "MERCHANT_WALLET" // aktif mulai Sprint 4
	KindProviderClearing AccountKind = "PROVIDER_CLEARING"
	KindBankClearing     AccountKind = "BANK_CLEARING"
	KindPayoutInTransit  AccountKind = "PAYOUT_IN_TRANSIT"
	KindFeeRevenue       AccountKind = "FEE_REVENUE"
)

// template: sisi debit dan kredit yang sah. Saldo wallet adalah LIABILITY perusahaan
// (uang milik user yang dititipkan): normal kredit, jadi KREDIT menambah saldo wallet dan
// DEBIT mengurangi. Inilah sumber kebingungan paling umum (risiko Sprint 2).
type template struct {
	SourceType  string
	Debit       []AccountKind
	Credit      []AccountKind
	AnyAccounts bool // ADJUSTMENT_MANUAL: ops memilih akun, tetap harus seimbang
	Reversal    bool // boleh punya reversal_of
}

var templates = map[Purpose]template{
	PurposeTopupCredit:        {SourceType: "topup", Debit: []AccountKind{KindProviderClearing}, Credit: []AccountKind{KindUserWallet}},
	PurposeTransfer:           {SourceType: "transfer", Debit: []AccountKind{KindUserWallet}, Credit: []AccountKind{KindUserWallet}},
	PurposePayment:            {SourceType: "payment_intent", Debit: []AccountKind{KindUserWallet}, Credit: []AccountKind{KindMerchantWallet}},
	PurposeRefund:             {SourceType: "refund", Debit: []AccountKind{KindMerchantWallet}, Credit: []AccountKind{KindUserWallet}},
	PurposePayoutHold:         {SourceType: "payout", Debit: []AccountKind{KindUserWallet}, Credit: []AccountKind{KindPayoutInTransit}},
	PurposePayoutSettle:       {SourceType: "payout", Debit: []AccountKind{KindPayoutInTransit}, Credit: []AccountKind{KindBankClearing}},
	PurposePayoutReversal:     {SourceType: "payout", Debit: []AccountKind{KindPayoutInTransit}, Credit: []AccountKind{KindUserWallet}, Reversal: true},
	PurposeProviderSettlement: {SourceType: "provider_settlement", Debit: []AccountKind{KindBankClearing}, Credit: []AccountKind{KindProviderClearing}},
	PurposeAdjustmentManual:   {SourceType: "ops_action", AnyAccounts: true, Reversal: true},
}

// allows memeriksa apakah akun berjenis k boleh berada di sisi dir untuk template ini.
func (t template) allows(dir Direction, k AccountKind) bool {
	if t.AnyAccounts {
		return true
	}
	side := t.Debit
	if dir == Credit {
		side = t.Credit
	}
	for _, s := range side {
		if s == k {
			return true
		}
	}
	return false
}

func kindForSystemCode(code string) AccountKind {
	switch code {
	case "PROVIDER_CLEARING":
		return KindProviderClearing
	case "BANK_CLEARING":
		return KindBankClearing
	case "PAYOUT_IN_TRANSIT":
		return KindPayoutInTransit
	case "FEE_REVENUE":
		return KindFeeRevenue
	}
	return ""
}

// TransactionType memetakan purpose ke tipe yang dilihat user di riwayat wallet.
func (p Purpose) TransactionType() string {
	switch p {
	case PurposeTopupCredit:
		return "TOPUP"
	case PurposeTransfer:
		return "TRANSFER"
	case PurposePayment:
		return "PAYMENT"
	case PurposeRefund:
		return "REFUND"
	case PurposePayoutHold, PurposePayoutSettle, PurposePayoutReversal:
		return "PAYOUT"
	default:
		return "ADJUSTMENT"
	}
}

// PurposesForType kebalikan TransactionType (untuk filter ?type=).
func PurposesForType(t string) []Purpose {
	var out []Purpose
	for p := range templates {
		if p.TransactionType() == t {
			out = append(out, p)
		}
	}
	return out
}
