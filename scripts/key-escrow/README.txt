Stowline - backing up the backup key            (Türkçe metin aşağıda)
=====================================

WHY?
Your backups are stored ENCRYPTED. The only copy of the key that opens them
is on this computer (in a file encrypted to Windows). If the disk fails or
Windows is reinstalled, the backups can never be opened again. This step
lets you keep a copy of the key somewhere safe.

NOTHING IS INSTALLED. The service keeps running, the key does not change and
backups are not affected. The tool only reads.

HOW?
1. Copy this folder (both files together) to the computer being backed up.
2. Double-click "Export-ResticKeyEscrow.cmd" and answer "Yes" to the
   permission prompt (administrator rights are needed).
3. Type YES and press Enter. The 64-character key is shown ONCE.
4. Save the key in TWO places, with different people/places:
     - a password manager (Bitwarden / 1Password / KeePass), and
     - on paper, in a sealed envelope in a safe.
   The "fingerprint" on screen (XXXX-XXXX-XXXX) is not the key itself; write
   it next to your copy so you can tell later which key it is.
5. Press Enter (the screen is cleared). The tool asks you to paste the copy
   you saved. You should see "MATCH". If it does not match, do not trust that
   copy; try again.
6. The tool asks "Also save a copy of the key in the server's Key Vault?
   (Y/N)". With "Y" the key is also kept on the server, ENCRYPTED; you can
   see it on the "Key Vault" page of the admin panel (after entering the
   administrator password). This is an extra copy, NOT a replacement for the
   paper/password-manager copies.
7. Close the window.

CAREFUL
- NEVER send the key by e-mail, messaging, chat, ticket or screenshot.
- Only run it at the computer itself or in a remote session you trust.
- To check your copy later, in PowerShell:
  Export-ResticKeyEscrow.ps1 -Verify
- If you already saved the key and only want to send it to the server:
  Export-ResticKeyEscrow.ps1 -UploadOnly

The tool speaks Turkish when Windows is set to Turkish, English otherwise.


Stowline - Anahtar (şifre) yedekleme
====================================

NEDEN?
Yedekleriniz ŞİFRELİ duruyor. Bu şifrenin (anahtarın) tek kopyası şu an
sadece bu bilgisayarda (Windows'a bağlı şifreli dosyada). Bilgisayarın diski
bozulur ya da Windows yeniden kurulursa yedekler kalıcı olarak açılamaz. Bu
adım, anahtarın bir kopyasını güvenli bir yere almanızı sağlar.

KURULUM YOK. Servis durmaz, anahtar değişmez, yedekleme etkilenmez. Sadece okur.

NASIL?
1. Bu klasörü (iki dosya birlikte) yedeklenen bilgisayara kopyalayın.
2. "Export-ResticKeyEscrow.cmd" dosyasına çift tıklayın, çıkan izin penceresine
   "Evet" deyin (Yönetici izni gerekir).
3. "EVET" yazıp Enter'a basın. 64 karakterlik anahtar BİR KEZ gösterilir.
4. Anahtarı İKİ yere kaydedin, farklı kişilerde/yerlerde:
     - parola yöneticisi (Bitwarden / 1Password / KeePass), ve
     - kâğıda yazıp mühürlü zarfla kasada saklayın.
   Ekrandaki "parmak izi" (XXXX-XXXX-XXXX) anahtarın kendisi değildir; kopyanın
   yanına yazın, ileride hangi anahtar olduğunu anlamak için.
5. Enter'a basın (ekran temizlenir). Program sizden kaydettiğiniz kopyayı
   yapıştırmanızı ister. "EŞLEŞTİ" görmelisiniz. Eşleşmediyse o kopyaya
   güvenmeyin, tekrar deneyin.
6. Program "Şifre Kasası'na da kaydedeyim mi? (E/H)" diye sorar. "E" derseniz
   anahtar sunucuda ŞİFRELİ bir kopya olarak saklanır; yönetim panelinde
   "Şifre Kasası" sayfasından (yönetici şifresini girerek) görebilirsiniz.
   Bu, kâğıt/parola yöneticisi kopyalarının YERİNE geçmez, ek bir kopyadır.
7. Pencereyi kapatın.

DİKKAT
- Anahtarı e-posta, mesajlaşma, sohbet, bilet veya ekran görüntüsü ile ASLA göndermeyin.
- Sadece bilgisayarın başında ya da güvendiğiniz bir uzak oturumda çalıştırın.
- Sonradan kopyanızı kontrol etmek için: PowerShell'de
  Export-ResticKeyEscrow.ps1 -Verify
- Anahtarı zaten kaydettiyseniz ve sadece sunucuya göndermek istiyorsanız:
  Export-ResticKeyEscrow.ps1 -UploadOnly
