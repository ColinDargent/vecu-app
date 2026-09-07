package main

// dialogues_ps.go : la CONSTRUCTION des scripts PowerShell, séparée de leur
// exécution.
//
// Séparés pour la même raison que les scripts AppleScript le sont dans
// dossier_darwin.go, et cette raison est mesurée : « un défaut ne se voit qu'en
// cliquant, et cliquer demande un humain devant l'écran ». Isolés ici, les
// scripts se font ANALYSER par le parseur de PowerShell dans un test, sans rien
// afficher - ce qui attrape la faute de syntaxe et l'échappement cassé, les deux
// seules classes qu'une relecture rate vraiment.
//
// CE FICHIER COMPILE SUR TOUS LES SYSTÈMES. L'échappement se teste donc depuis
// macOS ; l'analyse syntaxique, elle, demande un Windows - et tourne en CI.

import "strings"

// marqueurAnnule : l'annulation se reconnaît à un marqueur QUE NOUS ÉCRIVONS,
// jamais à un message du système.
//
// C'est la même leçon que le « (-128) » de la version macOS, tirée à l'avance
// plutôt qu'après coup : le poste de Colin est en français, Windows localise
// tout ce qu'il affiche, et un test écrit contre « Cancel » passerait au vert en
// laissant le vrai cas cassé. Ici la question ne se pose pas, parce que la
// chaîne comparée ne vient pas de Windows - elle vient de notre propre script.
const marqueurAnnule = "__VECU_ANNULE__"

// preambulePS : ce que tout dialogue charge avant de s'afficher.
//
// `proprietaireAuPremierPlan` existe pour un défaut précis : une boîte de
// dialogue sans fenêtre propriétaire s'ouvre DERRIÈRE la fenêtre active quand
// elle est demandée par un processus qui n'a pas le focus - et c'est exactement
// notre cas, l'app vivant dans la zone de notification. On lui donne donc un
// propriétaire invisible et TopMost. Même raison que l'`activate` d'AppleScript
// sur macOS, autre mécanique.
const preambulePS = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

function proprietaireAuPremierPlan {
	$o = New-Object System.Windows.Forms.Form
	$o.TopMost = $true
	$o.ShowInTaskbar = $false
	$o.FormBorderStyle = 'None'
	$o.Size = New-Object System.Drawing.Size(1, 1)
	$o.StartPosition = 'Manual'
	$o.Location = New-Object System.Drawing.Point(-4000, -4000)
	$o.Show()
	return $o
}

function nouvelleFenetre($hauteur) {
	$f = New-Object System.Windows.Forms.Form
	$f.Text = 'Vécu'
	$f.FormBorderStyle = 'FixedDialog'
	$f.StartPosition = 'CenterScreen'
	$f.MinimizeBox = $false
	$f.MaximizeBox = $false
	$f.TopMost = $true
	$f.ClientSize = New-Object System.Drawing.Size(460, $hauteur)
	$f.Font = New-Object System.Drawing.Font('Segoe UI', 9)
	return $f
}

function ajouteMessage($f, $texte) {
	$l = New-Object System.Windows.Forms.Label
	$l.Text = $texte
	$l.SetBounds(16, 16, 428, 48)
	$l.AutoSize = $false
	$f.Controls.Add($l)
	return $l
}

# Pose les boutons alignés à droite, du dernier au premier. Rend la fenêtre
# prête ; le libellé cliqué atterrit dans $script:reponse.
function ajouteBoutons($f, $libelles, $y) {
	$x = 444
	for ($i = $libelles.Count - 1; $i -ge 0; $i--) {
		$b = New-Object System.Windows.Forms.Button
		$b.Text = $libelles[$i]
		$b.AutoSize = $true
		$b.MinimumSize = New-Object System.Drawing.Size(96, 26)
		$b.Left = $x - $b.PreferredSize.Width
		$b.Top = $y
		$b.Width = $b.PreferredSize.Width
		$x = $b.Left - 8
		$libelle = $libelles[$i]
		$b.Add_Click({ $script:reponse = $libelle; $f.Close() }.GetNewClosure())
		$f.Controls.Add($b)
		if ($i -eq 0) { $f.AcceptButton = $b }
	}
}

$script:reponse = $null
`

// psLitteral protège une chaîne pour un littéral PowerShell entre apostrophes
// simples.
//
// Entre apostrophes simples, PowerShell n'interprète RIEN : ni `$variable`, ni
// les séquences à accent grave, ni les sous-expressions. Le seul caractère à
// traiter est l'apostrophe elle-même, qui se double. C'est ce qui rend sûr le
// passage d'un nom de dossier arbitraire, y compris s'il contient `$(...)`.
func psLitteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psTableau rend un littéral de tableau PowerShell à partir de chaînes Go.
func psTableau(entrees []string) string {
	morceaux := make([]string, len(entrees))
	for i, e := range entrees {
		morceaux[i] = psLitteral(e)
	}
	return "@(" + strings.Join(morceaux, ",") + ")"
}

// Les constructeurs de scripts, un par dialogue.
//
// Préfixés `ps` parce que dossier_darwin.go porte les mêmes sept fonctions pour
// AppleScript, et que ce fichier-ci compile sur tous les systèmes : sans le
// préfixe, les deux jeux se télescopent à la compilation sur macOS.

func psScriptChoisirDossier(prompt string) string {
	return `
$o = proprietaireAuPremierPlan
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = ` + psLitteral(prompt) + `
$d.ShowNewFolderButton = $true
$r = $d.ShowDialog($o)
$o.Close()
if ($r -eq [System.Windows.Forms.DialogResult]::OK -and $d.SelectedPath) {
	[Console]::Out.Write($d.SelectedPath)
} else {
	[Console]::Out.Write('` + marqueurAnnule + `')
}
`
}

func psScriptInforme(message string) string {
	return `
$o = proprietaireAuPremierPlan
[void][System.Windows.Forms.MessageBox]::Show($o, ` + psLitteral(message) + `, 'Vécu',
	[System.Windows.Forms.MessageBoxButtons]::OK,
	[System.Windows.Forms.MessageBoxIcon]::Information)
$o.Close()
`
}

// psScriptBoutons : le corps commun à `confirme` et `choix`.
//
// Fermer par la croix ou par Échap laisse `$script:reponse` à `$null`, donc rend
// le marqueur : les trois façons de renoncer (le bouton, la croix, Échap)
// arrivent au même endroit, exactement comme le « -128 » d'AppleScript les
// réunissait sur macOS.
func psScriptBoutons(message string, boutons []string) string {
	return `
$f = nouvelleFenetre 128
[void](ajouteMessage $f ` + psLitteral(message) + `)
ajouteBoutons $f ` + psTableau(boutons) + ` 84
# Échap doit renoncer, comme la croix. WinForms ne le fait PAS tout seul : sans
# CancelButton ni ce gestionnaire, la touche ne produit rien. Le commentaire de
# la fonction Go promettait « la croix ou Échap » alors que seule la croix
# marchait - corrigé ici plutôt que dans le commentaire.
$f.KeyPreview = $true
$f.Add_KeyDown({ if ($_.KeyCode -eq [System.Windows.Forms.Keys]::Escape) { $script:reponse = $null; $f.Close() } })
[void]$f.ShowDialog()
if ($null -eq $script:reponse) {
	[Console]::Out.Write('` + marqueurAnnule + `')
} else {
	[Console]::Out.Write($script:reponse)
}
`
}

func psScriptDemandeTexte(message, defaut string) string {
	return `
$f = nouvelleFenetre 160
[void](ajouteMessage $f ` + psLitteral(message) + `)
$t = New-Object System.Windows.Forms.TextBox
$t.SetBounds(16, 72, 428, 24)
$t.Text = ` + psLitteral(defaut) + `
$f.Controls.Add($t)
ajouteBoutons $f @('OK','Annuler') 116
$f.Add_Shown({ $t.Select(); $t.SelectAll() })
[void]$f.ShowDialog()
if ($script:reponse -eq 'OK') {
	[Console]::Out.Write($t.Text)
} else {
	[Console]::Out.Write('` + marqueurAnnule + `')
}
`
}

func psScriptChoisirDansListe(message string, entrees []string) string {
	return `
$f = nouvelleFenetre 320
[void](ajouteMessage $f ` + psLitteral(message) + `)
$l = New-Object System.Windows.Forms.ListBox
$l.SetBounds(16, 72, 428, 200)
foreach ($e in ` + psTableau(entrees) + `) { [void]$l.Items.Add($e) }
$l.SelectedIndex = 0
$l.Add_DoubleClick({ $script:reponse = 'OK'; $f.Close() })
$f.Controls.Add($l)
ajouteBoutons $f @('OK','Annuler') 282
[void]$f.ShowDialog()
if ($script:reponse -eq 'OK' -and $null -ne $l.SelectedItem) {
	[Console]::Out.Write($l.SelectedItem)
} else {
	[Console]::Out.Write('` + marqueurAnnule + `')
}
`
}

func psScriptDemandeMotDePasse(message string) string {
	return `
$f = nouvelleFenetre 160
[void](ajouteMessage $f ` + psLitteral(message) + `)
$t = New-Object System.Windows.Forms.TextBox
$t.SetBounds(16, 72, 428, 24)
$t.UseSystemPasswordChar = $true
$f.Controls.Add($t)
ajouteBoutons $f @('OK','Annuler') 116
$f.Add_Shown({ $t.Select() })
[void]$f.ShowDialog()
if ($script:reponse -eq 'OK') {
	[Console]::Out.Write($t.Text)
} else {
	[Console]::Out.Write('` + marqueurAnnule + `')
}
`
}
