// Проверяю гипотезу и оцениваю, как изменение правила повлияло бы на исторические чаты
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
)

type rule struct {
	name          string
	sourceIntents map[string]bool
	targetSkill   string
	required      []*regexp.Regexp // Все условия должны выполниться
	forbidden     []*regexp.Regexp // Ни одно исключение не должно сработать
}

// Гипотеза: вопрос о кэшбэке/бонусах по дебетовой карте,
// не содержащий признаков сервиса lifestyle, отправляем в dk_service
var debitCardCashback = rule{
	name: "debit_card_cashback_outside_lifestyle",
	sourceIntents: map[string]bool{
		"bizneslinija_lifestyle_postaid_regex":         true,
		"bizneslinija_nefinansovye_produkty_lifestyle": true,
		"bizneslinija_vopros_po_servisam_lajfstajl":    true,
	},
	targetSkill: "dk_service",
	required: []*regexp.Regexp{
		regexp.MustCompile(`(?i)(к[эе]ш\s*б[эе]к|бонус\p{L}*|мил\p{L}*)`),                      // признак кешбэка, бонусов, миль
		regexp.MustCompile(`(?i)(дебетов\p{L}*\s+карт\p{L}*|t\s*-?\s*black|тинькофф\s*black)`), // признак дебетовой карты
	},
	forbidden: []*regexp.Regexp{
		// признаки lifystyle
		regexp.MustCompile(`(?i)(город|топлив\p{L}*|заправ\p{L}*|вкусвилл|пят[её]рочк\p{L}*|перекр[её]сток|лент\p{L}*|самокат\p{L}*|юрент|афиш\p{L}*|кино|концерт\p{L}*|театр\p{L}*|цвет\p{L}*|ресторан\p{L}*|t\s*-?\s*shop|шопинг)`),
	},
}

type stats struct {
	total                int
	potentiallyPrevented int
	potentiallyHarmful   int
	other                int
}

func (s *stats) add(lastSkill, target, lifestyleSkill string) {
	s.total++
	switch lastSkill {
	case target:
		s.potentiallyPrevented++ // Перенаправленный чат по новому правилу
	case lifestyleSkill:
		s.potentiallyHarmful++ // Чат без изменения, тк перенаправление может быть ошибочным
	default:
		s.other++ // Кейсы, которые не получится надежно оценить автоматически
	}
}

func (s stats) print(label string) {
	if s.total == 0 {
		fmt.Printf("%s: коммуникаций не найдено\n", label)
		return
	}

	pct := func(n int) float64 {
		return 100 * float64(n) / float64(s.total)
	}

	balance := s.potentiallyPrevented - s.potentiallyHarmful

	fmt.Printf("%s\n", label)
	fmt.Printf("Всего коммуникаций: %d\n", s.total)

	fmt.Printf(
		"Потенциально предотвращённые ретрансферы: %d (%.1f%%)\n",
		s.potentiallyPrevented,
		pct(s.potentiallyPrevented),
	)

	fmt.Printf(
		"Потенциально ошибочные перенаправления: %d (%.1f%%)\n",
		s.potentiallyHarmful,
		pct(s.potentiallyHarmful),
	)

	fmt.Printf(
		"Неоднозначные случаи: %d (%.1f%%)\n",
		s.other,
		pct(s.other),
	)

	fmt.Printf(
		"Предварительный баланс: %+d\n",
		balance,
	)
}

func matches(r rule, intent, text string) bool {
	if !r.sourceIntents[intent] {
		return false
	}
	for _, re := range r.required {
		if !re.MatchString(text) {
			return false
		}
	}
	for _, re := range r.forbidden {
		if re.MatchString(text) {
			return false
		}
	}
	return true
}

func inSplit(id, split string) bool {
	if split == "all" {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	isTest := h.Sum32()%100 < 20 // Стабильное деление чатов на 80% и 20%
	return (split == "test" && isTest) || (split == "train" && !isTest)
}

func main() {
	input := flag.String("input", "lifestyle_eval.csv", "путь к CSV-файлу с историческими коммуникациями")
	split := flag.String("split", "all", "выборка для проверки: all, train или test")
	lifestyleSkill := flag.String("lifestyle-skill", "lifestyle", "значение lifestyle-skill в исходных данных")
	flag.Parse()
	if *split != "all" && *split != "train" && *split != "test" {
		log.Fatal("-split должен быть со значением all, train или test")
	}

	f, err := os.Open(*input)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	rows := csv.NewReader(f)
	head, err := rows.Read()
	if err != nil {
		log.Fatal(err)
	}
	index := map[string]int{}
	for i, col := range head {
		index[strings.TrimPrefix(strings.TrimSpace(col), "\ufeff")] = i
	}
	for _, col := range []string{"id", "text", "intent_key_code", "first_skill", "last_skill"} {
		if _, ok := index[col]; !ok {
			log.Fatalf("отсутствует CSV столбец %q", col)
		}
	}

	var source, changed stats
	for {
		record, err := rows.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		get := func(col string) string { return record[index[col]] }
		if !inSplit(get("id"), *split) || get("first_skill") != *lifestyleSkill || !debitCardCashback.sourceIntents[get("intent_key_code")] {
			continue
		}
		source.add(get("last_skill"), debitCardCashback.targetSkill, *lifestyleSkill)
		if matches(debitCardCashback, get("intent_key_code"), get("text")) {
			changed.add(get("last_skill"), debitCardCashback.targetSkill, *lifestyleSkill)
		}
	}

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Результат проверки гипотезы")
	fmt.Println("========================================")

	fmt.Printf(
		"Проверяемое правило: %s\n",
		debitCardCashback.name,
	)

	fmt.Printf(
		"Предлагаемый целевой skill: %s\n",
		debitCardCashback.targetSkill,
	)

	fmt.Printf(
		"Выборка: %s\n",
		*split,
	)

	fmt.Println()
	fmt.Println("1. Коммуникации, попавшие под исходные правила:")
	source.print("")

	fmt.Println()
	fmt.Println("2. Коммуникации, которые изменит новое правило:")
	changed.print("")

	fmt.Println()
	fmt.Println("Интерпретация:")

	fmt.Printf(
		"- %d коммуникаций по историческим данным завершились в %s — это потенциальные случаи, где новое правило могло бы предотвратить ретрансфер.\n",
		changed.potentiallyPrevented,
		debitCardCashback.targetSkill,
	)

	fmt.Printf(
		"- %d коммуникаций по историческим данным завершились в %s — это потенциальные случаи, где новое правило могло бы привести к ошибочному перенаправлению.\n",
		changed.potentiallyHarmful,
		*lifestyleSkill,
	)

	fmt.Printf(
		"- %d коммуникаций нельзя однозначно оценить по полю last_skill — их необходимо проверить отдельно.\n",
		changed.other,
	)

	if changed.total > 0 {
		balance :=
			changed.potentiallyPrevented -
				changed.potentiallyHarmful

		fmt.Println()

		switch {
		case balance > 0:
			fmt.Printf(
				"Предварительный результат: потенциальный положительный эффект +%d коммуникаций.\n",
				balance,
			)

		case balance < 0:
			fmt.Printf(
				"Предварительный результат: потенциальный отрицательный эффект %d коммуникаций.\n",
				balance,
			)

		default:
			fmt.Println(
				"Предварительный результат: потенциальный эффект правила нейтральный.",
			)
		}
	}
}
