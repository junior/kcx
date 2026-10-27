# Regenerate the README demo: bash .github/demo-setup.sh, then vhs .github/demo.tape
# (both from the repo root). The setup prepares a home of its own under /tmp/demo
# with a kind cluster behind every cluster of the example profile and Powerlevel10k
# in its pure style; its header also says how to record on a Mac whose Chrome quits
# headless after half a minute. The prompt's kube icon needs a Nerd Font installed.
# Keep this file pure ASCII: multibyte characters mojibake through ttyd typing.
Output .github/demo.gif

Set Shell zsh
Set FontFamily "MesloLGS Nerd Font"
Set FontSize 16
Set Width 920
Set Height 560
Set Padding 16
Set TypingSpeed 55ms
Set Theme "Dracula"

Hide
Type "export HOME=/tmp/demo && cd && exec zsh"
Enter
Sleep 2s
Type "clear"
Enter
Sleep 0.5s
Show

Sleep 1s
Type "# a profile lists your clusters; kcx fetches one by its short name"
Enter
Sleep 1s
Type "kcx profile ls"
Enter
Sleep 2.5s
Type "kcx prod1"
Enter
Sleep 4s
Type "# the picker: switch, or browse the profile by group and site"
Enter
Sleep 1s
Type "kcx"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Down
Sleep 1.5s
Enter
Sleep 2.5s
Enter
Sleep 4s
Type "kcx ns"
Enter
Sleep 2s
Type "pay"
Sleep 1.5s
Enter
Sleep 2.5s
Type "# kcx shell: a shell of its own; what changes inside stays inside"
Enter
Sleep 1s
Type "kcx shell office7-lab"
Enter
Sleep 4s
Type "kcx ns search"
Enter
Sleep 3s
Type "exit"
Enter
Sleep 2.5s
Type "# this terminal kept its own cluster and namespace"
Enter
Sleep 2s
Type "# read-only, for AI tools: a local proxy refuses writes before they leave"
Enter
Sleep 1s
Type "kcx -r prod1 -- kubectl get nodes"
Enter
Sleep 3s
Type "kcx -r prod1 -- kubectl delete namespace search"
Enter
Sleep 4.5s
