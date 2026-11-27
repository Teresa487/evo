// subagent_name.go —— 给子 agent 生成可读的显示名字。

package agent

import (
	"math/rand/v2"
	"strings"
	"sync"
)

// subagentFirstName pairs ASCII key 模型 types 用 accented
// form a person reads.
type subagentFirstName struct {
	key     string
	display string
}

// subagentFirstNames 是 borrowed 从 charmbracelet/creeper.
var subagentFirstNames = []subagentFirstName{
	{"akiko", "Akiko"},
	{"andre", "André"},
	{"amy", "Amy"},
	{"alejandra", "Alejandra"},
	{"alexander", "Alexander"},
	{"athena", "Athena"},
	{"benedict", "Benedict"},
	{"benoit", "Benoît"},
	{"betty", "Betty"},
	{"bianca", "Bianca"},
	{"bjorn", "Björn"},
	{"boris", "Boris"},
	{"candice", "Candice"},
	{"candy", "Candy"},
	{"celeste", "Céleste"},
	{"carlos", "Carlos"},
	{"chuck", "Chuck"},
	{"cindy", "Cindy"},
	{"cleo", "Cleo"},
	{"connie", "Connie"},
	{"constance", "Constance"},
	{"domina", "Domina"},
	{"enzo", "Enzo"},
	{"emily", "Emily"},
	{"felipe", "Felipe"},
	{"francis", "Francis"},
	{"freddy", "Freddy"},
	{"geraldo", "Geraldo"},
	{"gunnar", "Gunnar"},
	{"gustavo", "Gustavo"},
	{"jacopo", "Jacopo"},
	{"jan", "Jan"},
	{"javier", "Javier"},
	{"joakim", "Joakim"},
	{"karen", "Karen"},
	{"kieran", "Kieran"},
	{"linda", "Linda"},
	{"luka", "Luka"},
	{"madeline", "Madeline"},
	{"maria", "María"},
	{"massimo", "Massimo"},
	{"mateus", "Mateus"},
	{"nancy", "Nancy"},
	{"noemie", "Noémie"},
	{"mindy", "Mindy"},
	{"nicolas", "Nícolas"},
	{"paulo", "Paulo"},
	{"pepper", "Pepper"},
	{"rafael", "Rafael"},
	{"roberto", "Roberto"},
	{"sarah", "Sarah"},
	{"sasha", "Sasha"},
	{"saskia", "Saskia"},
	{"simon", "Simon"},
	{"sofia", "Sofía"},
	{"tiago", "Tiago"},
	{"valentina", "Valentina"},
	{"xochitl", "Xóchitl"},
}

// subagentNamer cycles through one shuffle 的 first names, 所以 name
// never repeats until every other one has been used.
type subagentNamer struct {
	once  sync.Once
	mu    sync.Mutex
	order []int
	next  int
}

// name puts next first name 在 front 的 label: "date-impl" becomes
// something like "betty-date-impl".
func (n *subagentNamer) name(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	n.once.Do(func() { n.order = rand.Perm(len(subagentFirstNames)) })
	n.mu.Lock()
	first := subagentFirstNames[n.order[n.next%len(n.order)]]
	n.next++
	n.mu.Unlock()
	return first.key + "-" + label
}

func splitSubagentName(name string) (subagentFirstName, string, bool) {
	for _, first := range subagentFirstNames {
		if rest, ok := strings.CutPrefix(name, first.key+"-"); ok && rest != "" {
			return first, rest, true
		}
	}
	return subagentFirstName{}, name, false
}

// SubagentDisplayName splits 子代理's name into 它的 accented first name
// 和 它的 label. 一个 label without first name comes back 用 first empty.
func SubagentDisplayName(name string) (first, label string) {
	f, label, _ := splitSubagentName(name)
	return f.display, label
}
