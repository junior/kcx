# Regenerate the README demo: bash .github/demo-setup.sh, then vhs .github/demo.tape
# (both from the repo root). The setup prepares a home of its own under /tmp/demo
# with a kind cluster behind every cluster of the example profile; its header also
# says how to record on a Mac whose Chrome quits headless after half a minute.
# Keep this file pure ASCII: multibyte characters mojibake through ttyd typing.
Output .github/demo.gif

Set Shell zsh
Set FontSize 16
Set Width 920
Set Height 520
Set Padding 16
Set TypingSpeed 55ms
Set Theme "Dracula"

Hide
Type "export HOME=/tmp/demo && cd && exec zsh"
Enter
Sleep 1s
Type "clear"
Enter
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
Type "# one file per cluster; the state file alone holds the current context"
Enter
Sleep 1s
Type "kcx ls"
Enter
Sleep 2.5s
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
Type "# namespaces have a picker too, remembered per context"
Enter
Sleep 1s
Type "kcx ns"
Enter
Sleep 2s
Type "pay"
Sleep 1.5s
Enter
Sleep 2.5s
Type "# read-only, for AI tools: a local proxy refuses writes before they leave"
Enter
Sleep 1s
Type "kcx -r prod1 -- kubectl get nodes"
Enter
Sleep 3s
Type "kcx -r prod1 -- kubectl delete namespace search"
Enter
Sleep 3.5s
Type "# one command against one cluster; the shared current context stays"
Enter
Sleep 1s
Type "kcx run prod1 -- kubectl config current-context"
Enter
Sleep 2.5s
Type "kcx -c"
Enter
Sleep 3.5s
