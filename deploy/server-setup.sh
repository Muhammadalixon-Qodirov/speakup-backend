#!/usr/bin/env bash
# ======================================================================
#  BIR MARTA ishga tushiriladi — serverni GitHub deploy'ga tayyorlaydi.
#  Hech narsani o'chirmaydi, konteynerlarni qayta ishga tushirmaydi.
#
#  Ishlatish:   ssh root@169.58.17.122 'bash -s' < deploy/server-setup.sh
# ======================================================================
set -euo pipefail

APP_DIR=/root/speakup
GH_USER=Muhammadalixon-Qodirov
REPO_SSH="git@github.com:${GH_USER}/speakup-backend.git"
BRANCH=main

cd "$APP_DIR"

echo "#####################################################################"
echo "#  1-qadam: .env ga POSTGRES_PASSWORD qo'shish"
echo "#####################################################################"
if grep -q '^POSTGRES_PASSWORD=' .env; then
  echo "  allaqachon bor — o'tkazib yuborildi"
else
  cp -a .env ".env.before_gitops_$(date +%F_%H-%M)"
  P=$(grep -oP '(?<=^DATABASE_URL=postgres://speakup:)[^@]+' .env)
  [ -n "$P" ] || { echo "XATO: DATABASE_URL dan parol ajratib olinmadi"; exit 1; }
  printf '\n# docker-compose.yml postgres xizmati uchun (DATABASE_URL bilan bir xil)\nPOSTGRES_PASSWORD=%s\n' "$P" >> .env
  unset P
  echo "  qo'shildi (zaxira: .env.before_gitops_*)"
fi

echo
echo "#####################################################################"
echo "#  2-qadam: server -> GitHub kaliti (private reponi pull qilish uchun)"
echo "#####################################################################"
if [ -f /root/.ssh/id_speakup_deploy ]; then
  echo "  kalit allaqachon bor — qaytadan yaratilmadi"
else
  ssh-keygen -t ed25519 -N '' -f /root/.ssh/id_speakup_deploy -C "speakup-server-pull" >/dev/null
  echo "  yangi kalit yaratildi"
fi
if ! grep -q 'Host github.com' /root/.ssh/config 2>/dev/null; then
  cat >> /root/.ssh/config <<'SSHCFG'

Host github.com
    IdentityFile /root/.ssh/id_speakup_deploy
    IdentitiesOnly yes
SSHCFG
  echo "  ~/.ssh/config ga github.com yozuvi qo'shildi"
fi
chmod 600 /root/.ssh/config
ssh-keyscan -t rsa,ecdsa,ed25519 github.com >> /root/.ssh/known_hosts 2>/dev/null
sort -u -o /root/.ssh/known_hosts /root/.ssh/known_hosts

echo
echo "#####################################################################"
echo "#  3-qadam: GitHub Actions -> server kaliti"
echo "#####################################################################"
if [ -f /root/.ssh/id_gh_actions ]; then
  echo "  kalit allaqachon bor — qaytadan yaratilmadi"
else
  ssh-keygen -t ed25519 -N '' -f /root/.ssh/id_gh_actions -C "github-actions-deploy" >/dev/null
  echo "  yangi kalit yaratildi"
fi
touch /root/.ssh/authorized_keys
if grep -qF "$(cat /root/.ssh/id_gh_actions.pub)" /root/.ssh/authorized_keys; then
  echo "  authorized_keys da allaqachon bor"
else
  cat /root/.ssh/id_gh_actions.pub >> /root/.ssh/authorized_keys
  echo "  authorized_keys ga qo'shildi"
fi
chmod 600 /root/.ssh/authorized_keys

echo
echo "#####################################################################"
echo "#  4-qadam: $APP_DIR ni git repoga aylantirish"
echo "#####################################################################"
# Papka 'ubuntu' foydalanuvchisiga tegishli, biz esa root sifatida
# ishlayapmiz. Git bunday holatda "dubious ownership" deb to'xtaydi.
# Shu papkani ishonchli deb belgilaymiz (faqat shuni, hammasini emas).
if git config --global --get-all safe.directory 2>/dev/null | grep -qx "$APP_DIR"; then
  echo "  safe.directory allaqachon sozlangan"
else
  git config --global --add safe.directory "$APP_DIR"
  echo "  safe.directory qo'shildi ($APP_DIR)"
fi

if [ -d .git ]; then
  echo "  allaqachon git repo"
  git remote set-url origin "$REPO_SSH" 2>/dev/null || git remote add origin "$REPO_SSH"
else
  git init -q
  git remote add origin "$REPO_SSH"
  echo "  git init bajarildi, origin qo'shildi"
fi
git config user.name  "SpeakUp Server"
git config user.email "server@speak-up.uz"

echo
echo "#####################################################################"
echo "#  SOZLASH TUGADI. Endi GitHub tomonida 2 ta ish bor."
echo "#####################################################################"
echo
echo "A) Repo -> Settings -> Deploy keys -> 'Add deploy key'"
echo "   Nomi: speakup-server     |  'Allow write access' BELGILANMAYDI"
echo "   Kalit (ochiq, maxfiy emas):"
echo "   ---------------------------------------------------------------"
cat /root/.ssh/id_speakup_deploy.pub
echo "   ---------------------------------------------------------------"
echo
echo "B) Repo -> Settings -> Secrets and variables -> Actions -> 4 ta secret:"
echo
echo "   DEPLOY_HOST = 169.58.17.122"
echo "   DEPLOY_USER = root"
echo
echo "   DEPLOY_KNOWN_HOSTS = (quyidagi qator):"
echo "   ---------------------------------------------------------------"
ssh-keyscan -t ed25519 169.58.17.122 2>/dev/null | head -1
echo "   ---------------------------------------------------------------"
echo
echo "   DEPLOY_SSH_KEY = shaxsiy kalit. Xavfsizlik uchun bu skript uni"
echo "   ekranga CHIQARMAYDI. Alohida ko'chirib oling:"
echo
echo "       ssh root@169.58.17.122 cat /root/.ssh/id_gh_actions"
echo
echo "   Chiqqan matnni BEGIN va END qatorlari bilan BIRGA, to'liq"
echo "   nusxalab secret'ga joylang."
echo
echo "Yuqoridagi A va B bajarilgach, oxirgi bog'lash qadami:"
echo
echo "       ssh root@169.58.17.122 'cd $APP_DIR && git fetch origin && git reset --mixed origin/$BRANCH && git status --short | head'"
echo
echo "Hech bir konteyner qayta ishga tushirilmadi — xizmat uzilmadi."
