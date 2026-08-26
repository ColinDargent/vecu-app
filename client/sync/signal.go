package sync

// signal.go : F4 - on apprend qu'un collègue a écrit un skill.
//
// Depuis le 28/07 un skill naît privé, propriété de son auteur. Le choix est bon
// et il a été pris après un incident. Sa conséquence non traitée : personne
// n'apprend jamais qu'un skill existe, donc personne ne demande à le voir, donc
// rien ne se partage. Le serveur rend l'existence ; ce fichier décide de ce qui
// est NOUVEAU pour ce poste, et de ce qui a déjà été dit.

import "sort"

// Signale interroge le serveur et met à jour ce que ce poste a à annoncer.
//
// Best-effort par construction, comme l'adoption et la projection : un serveur
// qui ne connaît pas encore l'endpoint (fenêtre de bascule, au moins une
// version) rend une erreur, et un signal manquant n'est jamais un échec de
// cycle. C'est aussi la leçon de F6, prise le matin même : une information qui
// fait sortir une commande en erreur apprend à ignorer la commande.
func (e *Engine) Signale() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	distants, err := e.client.SkillsNouveaux()
	if err != nil {
		return err
	}

	// AMORCE. Au premier cycle après la mise à jour, le registre est absent :
	// tous les skills déjà déposés sortiraient comme nouveaux. Chez Achille, ce
	// serait « 24 nouveaux skills » d'un coup - exactement le rapport qui crie au
	// loup que F6 vient de fermer. On enregistre sans rien annoncer, comme le
	// grandfathering de la migration F3 le 28/07 : on gèle l'existant, puis on
	// signale ce qui arrive après.
	//
	// `nil` et non `len() == 0` : une liste vide légitime (aucun skill d'autrui
	// au moment de l'amorce) ne doit pas relancer l'amorce à chaque cycle, sinon
	// le premier dépôt d'Achille serait enregistré sans jamais être annoncé.
	if e.state.SkillsVus == nil {
		e.state.SkillsVus = slugsDe(distants) // toujours non nul : voir le champ
		e.nouveaux = nil
		e.logf("signal des skills amorcé sur %d skill(s) déjà déposé(s) : seuls les dépôts à venir seront annoncés", len(e.state.SkillsVus))
		return e.state.Save(e.dir)
	}

	vus := make(map[string]bool, len(e.state.SkillsVus))
	for _, s := range e.state.SkillsVus {
		vus[s] = true
	}
	var nouveaux []NouveauSkill
	for _, n := range distants {
		if !vus[n.Slug] {
			nouveaux = append(nouveaux, n)
		}
	}
	for _, n := range nouveaux {
		if !estDejaSignale(e.nouveaux, n.Slug) {
			e.logf("nouveau skill chez %s : %s", n.Auteur, n.Slug)
		}
	}
	// Remplacé et non accumulé : un skill dont l'accès vient d'être retiré, ou
	// dont le compte auteur a disparu, cesse d'être annoncé au cycle suivant.
	e.nouveaux = nouveaux
	return nil
}

// NouveauxSkills : ce que ce poste a appris et que personne n'a encore lu.
func (e *Engine) NouveauxSkills() []NouveauSkill {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]NouveauSkill(nil), e.nouveaux...)
}

// MarqueSkillsVus : la personne a lu le signal. Les slugs entrent au registre et
// ne seront plus annoncés.
//
// Geste explicite, jamais automatique : un signal qui s'efface tout seul au
// cycle suivant n'aurait aucune chance d'être vu, le poll tournant toutes les
// quinze secondes.
func (e *Engine) MarqueSkillsVus() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.nouveaux) == 0 {
		return nil
	}
	for _, n := range e.nouveaux {
		e.state.SkillsVus = append(e.state.SkillsVus, n.Slug)
	}
	sort.Strings(e.state.SkillsVus)
	e.nouveaux = nil
	return e.state.Save(e.dir)
}

func slugsDe(ns []NouveauSkill) []string {
	out := make([]string, 0, len(ns)) // non nul même vide : l'amorce doit se voir
	for _, n := range ns {
		out = append(out, n.Slug)
	}
	sort.Strings(out)
	return out
}

func estDejaSignale(ns []NouveauSkill, slug string) bool {
	for _, n := range ns {
		if n.Slug == slug {
			return true
		}
	}
	return false
}
