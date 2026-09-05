# xray-optimized PLAN

Kural: kritik her bilgi 2 kaynaktan kontrol edilir.

## Setup (netleşti - final)
- Server: Istanbul DC / Netlen, Debian 13, public IP, CGNAT yok, sınırsız erişim, IPv4+IPv6 var
- In: IPv4:443, Out: IPv4/IPv6 (freedom dual-stack)
- Cihaz: max 3 (Android+Win+Linux), tek ortak UUID + tek shortId
- Politika: %100 TUN, bypass YOK, WhatsApp dahil her şey tünel içi
- In: IPv4:443 only (client v4), Out: dual v4/v6 (client v6 out OK)
- Dest: SADECE whatsapp.net ailesi, fallback YOK (kabul edilen risk: dest bozulursa net gider)
- Core: FORK DemirSe/xray-optimized-core (parent XTLS/Xray-core, pin stabil v26.3.27). Günlük kural: upstream tag'i takip et, core diff'i最小 tut (gün-1: sadece build+config, mantık değişikliği yok)
- Log: optimize bitene kadar açık (warning+access), sonra kapatılacak
- SSH: şifre ile ilk giriş, hemen key'e geçilecek

## Çift-kontrol bulgular
1. REALITY dest kriteri: TLS1.3 + h2, redirect yok, server'a network-yakın IP, ServerHello sonrası şifreli. Kaynak: XTLS REALITY README + Xray-docs-next reality.md
2. `web.whatsapp.com:443`: TLS1.3 var + ALPN h2 var, cert SAN `whatsapp.net + *.whatsapp.net`. Yani dest olarak plausible. Kaynak: testtls.com + WhatsApp FAQ (443). RISK: Meta CDN/anycast, bölgeye göre değişir -> server'dan `xray tls ping` ile teyit şart.
3. Domain gerekmez: REALITY IP + SNI ile çalışır, cert istemez. Kullanıcının "domain gerek yok" iddiası DOĞRU.
4. Client: 3 OS'ta tek aile = Hiddify (sing-box core, VLESS+REALITY+TUN). Alternatif v2rayNG (Android) + Throne/Nekoray (Win/Lin) ama 2 stack = 2x bakım. Az bakım için Hiddify hepsinde.
5. TUN: Android tek VPN izni, Win/Lin admin ister. DNS remote `tcp://1.1.1.1`, Local DNS TUN bozar. UDP, TCP tünel içinde taşınır (oyun/VoIP çalışır, latency artar).

## Plan
0. Server'da dest seç: `xray tls ping web.whatsapp.com:443` + `whatsapp.net:443` + `www.whatsapp.com:443`, en temizini al. serverNames/dest ona göre.
1. Server: resmi XTLS/Xray-install, VLESS `xtls-rprx-vision`, `target=SECILEN_DEST`, `serverNames=[SECILEN_SNI]`, shortId random 8byte, fp=chrome, 443 TCP dinle, ufw/firewall aç, BBR aç.
2. Tek vless:// link üret (uuid, pbk, sid, sni, fp=chrome, flow=vision).
3. Client 3 OS: Hiddify, TUN ON + System Proxy ON, Strict Route ON, IPv4-only (başta), Remote DNS tcp://1.1.1.1, UDP enabled. WhatsApp bypass YOK (önce içinden dene).
4. Test: ifconfig.me, dnsleaktest, WhatsApp mesaj + sesli + görüntülü, UDP test, speedtest. Bozulursa fallback: WhatsApp per-app bypass.

## Fallback
- WhatsApp call kötü ise: Android per-app bypass (Bypass mode + WhatsApp seç), Win/Lin route rule `whatsapp.net,whatsapp.com direct`.
- Dest bozulursa: `www.microsoft.com` gibi stabil dest'e geç (1 komut).

## Sonraki adım
Server SSH hazır mı? Hazırsa dest ping + kurulum komutunu veriyorum.
