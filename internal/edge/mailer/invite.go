package mailer

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode"
)

//go:embed assets/drawspace-icon.png
var iconPNG []byte

//go:embed assets/header-decoration.png
var decorationPNG []byte

// Invite is the data of one share-invitation email.
type Invite struct {
	ToName            string
	InviterName       string
	InviterUsername   string
	RecipientUsername string
	DocTitle          string
	Role              string     // "viewer" | "editor"
	ExpiresAt         *time.Time // nil = never expires
	Link              string
	SiteHost          string
}

// Rendered is the finished subject and bodies plus the inline images.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Inline  []InlineImage
}

const (
	subjectMaxRunes = 80
	neverExpires    = "Tidak kedaluwarsa"
)

var idMonths = [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}

// wib is Asia/Jakarta (UTC+7, no DST); a fixed zone avoids a tzdata dependency.
var wib = time.FixedZone("WIB", 7*60*60)

func roleInfo(role string) (label, desc string) {
	if strings.EqualFold(strings.TrimSpace(role), "editor") {
		return "Editor", "Dapat melihat dan mengubah diagram."
	}
	return "Viewer", "Dapat melihat diagram."
}

func formatDate(t time.Time) string {
	t = t.In(wib)
	return fmt.Sprintf("%d %s %d", t.Day(), idMonths[t.Month()-1], t.Year())
}

func formatExpiry(t *time.Time) string {
	if t == nil {
		return neverExpires
	}
	l := t.In(wib)
	return fmt.Sprintf("%s, %02d.%02d WIB", formatDate(l), l.Hour(), l.Minute())
}

func sanitizeSubjectTitle(title string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > subjectMaxRunes {
		s = string(r[:subjectMaxRunes]) + "…"
	}
	return s
}

type inviteView struct {
	Invite
	RoleLabel, RoleDesc string
	Expiry, Preheader   string
	SiteURL             string
}

var htmlTmpl = template.Must(template.New("invite").Parse(inviteHTML))

// RenderInvite builds the invitation email. All HTML goes through
// html/template auto-escaping.
func RenderInvite(in Invite) (Rendered, error) {
	label, desc := roleInfo(in.Role)
	v := inviteView{Invite: in, RoleLabel: label, RoleDesc: desc, Expiry: formatExpiry(in.ExpiresAt)}
	if h := strings.TrimSpace(in.SiteHost); h != "" {
		v.SiteURL = "https://" + h
	} else {
		v.SiteURL = "#"
	}
	if v.Link == "" {
		v.Link = "#"
	}
	v.Preheader = fmt.Sprintf("%s mengundang Anda sebagai %s pada diagram \"%s\". ", in.InviterName, label, in.DocTitle)
	if in.ExpiresAt != nil {
		v.Preheader += "Berlaku hingga " + formatDate(*in.ExpiresAt) + "."
	} else {
		v.Preheader += neverExpires + "."
	}
	var buf bytes.Buffer
	if err := htmlTmpl.Execute(&buf, v); err != nil {
		return Rendered{}, fmt.Errorf("render invite html: %w", err)
	}
	greet := strings.TrimSpace(in.ToName)
	if greet == "" {
		greet = "Bapak/Ibu"
	}
	var t strings.Builder
	fmt.Fprintf(&t, "Yth. %s,\r\n\r\n", greet)
	fmt.Fprintf(&t, "%s mengundang Anda untuk bekerja pada diagram berikut di DrawSpace. Akses baru diberikan setelah Anda menyetujui undangan ini.\r\n\r\n", in.InviterName)
	t.WriteString("Detail Undangan\r\n")
	fmt.Fprintf(&t, "Diagram       : %s\r\n", in.DocTitle)
	fmt.Fprintf(&t, "Pemilik       : %s (%s)\r\n", in.InviterName, in.InviterUsername)
	fmt.Fprintf(&t, "Hak akses     : %s - %s\r\n", label, desc)
	fmt.Fprintf(&t, "Berlaku hingga: %s\r\n\r\n", v.Expiry)
	fmt.Fprintf(&t, "Perlu Diketahui\r\nUndangan ini hanya dapat dibuka dengan akun %s. Diagram baru muncul di daftar Anda setelah Anda menekan Terima; Anda juga dapat menekan Tolak bila undangan ini tidak sesuai.\r\n\r\n", in.RecipientUsername)
	fmt.Fprintf(&t, "Lihat Undangan: %s\r\n\r\n", in.Link)
	t.WriteString("Jika Anda tidak mengenal pengirim undangan ini, abaikan email ini; tidak ada akses yang diberikan sebelum Anda menerimanya.\r\nEmail ini dikirim otomatis oleh sistem. Mohon tidak membalas email ini.\r\n")
	if in.SiteHost != "" {
		fmt.Fprintf(&t, "\r\nDrawSpace - %s\r\n", in.SiteHost)
	}
	return Rendered{
		Subject: "Undangan akses diagram '" + sanitizeSubjectTitle(in.DocTitle) + "'",
		HTML:    buf.String(),
		Text:    t.String(),
		Inline: []InlineImage{
			{CID: "drawspace-icon", ContentType: "image/png", Data: iconPNG},
			{CID: "drawspace-header-decoration", ContentType: "image/png", Data: decorationPNG},
		},
	}, nil
}

const inviteHTML = `<!DOCTYPE html>
<html lang="id"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Undangan akses diagram DrawSpace</title>
<style>@media only screen and (max-width:520px){.deco{display:none !important}.px{padding-left:16px !important;padding-right:16px !important}.lbl{width:104px !important}}</style></head>
<body style="margin:0;padding:0;background:#F8FAFD;color:#111827;font-family:Inter,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">{{.Preheader}}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#F8FAFD;"><tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="640" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:640px;background:#ffffff;border:1px solid #D9E2F0;border-radius:10px;overflow:hidden;">

<!-- HEADER -->
<tr><td style="padding:0;border-bottom:1px solid #D9E2F0;background:#ffffff;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>
<td class="px" valign="middle" style="padding:16px 0 16px 28px;"><table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
<td valign="middle" width="56" style="width:56px;"><img src="cid:drawspace-icon" width="52" height="52" alt="DrawSpace" style="display:block;width:52px;height:52px;border:0;"></td>
<td valign="middle" style="padding-left:12px;"><div style="font-size:22px;font-weight:700;color:#1463F3;line-height:28px;">Draw<span style="color:#111827;">Space</span></div>
<div style="font-size:12px;color:#6B7A90;line-height:16px;margin-top:1px;letter-spacing:.12em;">DESIGN · CONNECT · COLLABORATE</div></td></tr></table></td>
<td class="deco" valign="bottom" align="right" width="170" style="width:170px;padding:0;line-height:0;font-size:0;">
<img src="cid:drawspace-header-decoration" width="170" height="66" alt="" style="display:block;width:170px;height:66px;border:0;"></td>
</tr></table></td></tr>

<!-- HERO -->
<tr><td class="px" style="padding:24px 28px 0 28px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#EEF4FF;border:1px solid #C9DBFF;border-left:4px solid #1463F3;border-radius:8px;"><tr>
<td valign="middle" width="52" style="width:52px;padding:14px 0 14px 16px;"><div style="width:36px;height:36px;border-radius:50%;background:#1463F3;color:#fff;text-align:center;line-height:36px;font-size:18px;">✉</div></td>
<td valign="middle" style="padding:14px 16px 14px 12px;"><div style="font-size:11px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:#1463F3;">Undangan Akses Diagram</div>
<div style="font-size:17px;font-weight:700;color:#111827;line-height:23px;margin-top:2px;">{{.DocTitle}}</div></td></tr></table></td></tr>

<!-- SAPAAN -->
<tr><td class="px" style="padding:20px 28px 4px 28px;font-size:14px;line-height:22px;color:#111827;">
<p style="margin:0 0 8px 0;">Yth. {{if .ToName}}{{.ToName}}{{else}}Bapak/Ibu{{end}},</p>
<p style="margin:0 0 8px 0;"><strong>{{.InviterName}}</strong> mengundang Anda untuk bekerja pada diagram berikut di DrawSpace. Akses baru diberikan setelah Anda menyetujui undangan ini.</p></td></tr>

<!-- DETAIL -->
<tr><td class="px" style="padding:12px 28px 0 28px;">
<div style="font-size:14px;font-weight:700;color:#111827;margin:0 0 8px 0;">Detail Undangan</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#F8FAFD;border:1px solid #D9E2F0;border-radius:8px;">
<tr><td class="lbl" valign="top" width="150" style="width:150px;padding:10px 12px 10px 16px;font-size:13px;color:#6B7A90;">Diagram</td><td valign="top" style="padding:10px 16px 10px 0;font-size:13px;color:#111827;font-weight:600;">{{.DocTitle}}</td></tr>
<tr><td class="lbl" valign="top" style="padding:10px 12px 10px 16px;font-size:13px;color:#6B7A90;border-top:1px solid #D9E2F0;">Pemilik</td><td valign="top" style="padding:10px 16px 10px 0;font-size:13px;color:#111827;font-weight:600;border-top:1px solid #D9E2F0;">{{.InviterName}}<div style="font-weight:400;color:#5B6B84;margin-top:2px;">{{.InviterUsername}}</div></td></tr>
<tr><td class="lbl" valign="top" style="padding:10px 12px 10px 16px;font-size:13px;color:#6B7A90;border-top:1px solid #D9E2F0;">Hak akses</td><td valign="top" style="padding:10px 16px 10px 0;font-size:13px;border-top:1px solid #D9E2F0;"><span style="display:inline-block;padding:2px 10px;border-radius:999px;background:#E6EEFF;color:#1463F3;font-size:12px;font-weight:700;">{{.RoleLabel}}</span><div style="font-weight:400;color:#5B6B84;margin-top:4px;">{{.RoleDesc}}</div></td></tr>
<tr><td class="lbl" valign="top" style="padding:10px 12px 10px 16px;font-size:13px;color:#6B7A90;border-top:1px solid #D9E2F0;">Berlaku hingga</td><td valign="top" style="padding:10px 16px 10px 0;font-size:13px;color:#111827;font-weight:600;border-top:1px solid #D9E2F0;">{{.Expiry}}</td></tr>
</table></td></tr>

<!-- KEAMANAN -->
<tr><td class="px" style="padding:16px 28px 0 28px;">
<div style="font-size:14px;font-weight:700;color:#111827;margin:0 0 8px 0;">Perlu Diketahui</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#FFF8E8;border:1px solid #F3DDA3;border-radius:8px;"><tr><td style="padding:12px 16px;font-size:13px;line-height:20px;color:#111827;">
Undangan ini hanya dapat dibuka dengan akun <strong>{{.RecipientUsername}}</strong>. Diagram baru muncul di daftar Anda setelah Anda menekan <strong>Terima</strong>; Anda juga dapat menekan <strong>Tolak</strong> bila undangan ini tidak sesuai.</td></tr></table></td></tr>

<!-- CTA -->
<tr><td class="px" style="padding:24px 28px 0 28px;"><table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
<td bgcolor="#1463F3" style="background:#1463F3;border-radius:6px;"><a href="{{.Link}}" style="display:inline-block;padding:11px 18px;font-size:13px;font-weight:700;line-height:18px;color:#ffffff;text-decoration:none;border-radius:6px;">Lihat Undangan &rarr;</a></td></tr></table></td></tr>

<!-- PENUTUP -->
<tr><td class="px" style="padding:20px 28px 24px 28px;font-size:12px;line-height:18px;color:#6B7A90;">
Jika Anda tidak mengenal pengirim undangan ini, abaikan email ini; tidak ada akses yang diberikan sebelum Anda menerimanya.<br>Email ini dikirim otomatis oleh sistem. Mohon tidak membalas email ini.</td></tr>

<!-- FOOTER -->
<tr><td style="padding:0;border-top:1px solid #D9E2F0;background:#F8FAFD;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>
<td class="px" valign="middle" width="28" style="width:28px;padding:16px 0 16px 28px;"><img src="cid:drawspace-icon" width="28" height="28" alt="DrawSpace" style="display:block;width:28px;height:28px;border:0;"></td>
<td valign="middle" style="padding:16px 0 16px 10px;"><div style="font-size:13px;font-weight:700;color:#111827;">DrawSpace</div><div style="font-size:12px;color:#6B7A90;line-height:18px;">Ruang kerja diagram dan kolaborasi untuk seluruh tim.</div></td>
<td valign="middle" align="right" style="padding:16px 28px 16px 12px;font-size:12px;white-space:nowrap;"><a href="{{.SiteURL}}" style="color:#1463F3;text-decoration:none;">{{.SiteHost}}</a></td>
</tr></table></td></tr>

</table></td></tr></table></body></html>
`
