package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
)

// runProbe — режим диагностики: делает ОДИН запрос к API для указанного ID,
// печатает HTTP-статус, размер и тело ответа целиком в STDOUT, и выходит.
// Используется чтобы увидеть реальную форму JSON-ответа SCTS API.
func runProbe(cli *Client, method, idParam string, id int) {
	params := url.Values{}
	params.Set(idParam, strconv.Itoa(id))

	fmt.Fprintf(os.Stderr, "PROBE: POST %s/api.php  body=action[0]=%s&%s=%d\n\n",
		cli.cfg.BaseURL, method, idParam, id)

	status, body, err := cli.Call(method, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА сети: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "HTTP %d, размер: %d байт\n", status, len(body))
	fmt.Fprintf(os.Stderr, "---  RAW BODY  ---\n")
	os.Stdout.Write(body)
	fmt.Fprintf(os.Stderr, "\n---  END BODY  ---\n")

	stripped := stripJHRWrapper(body)
	if string(stripped) != string(body) {
		fmt.Fprintf(os.Stderr, "\n---  ПОСЛЕ СНЯТИЯ JHR-WRAPPER  ---\n")
		os.Stdout.Write(stripped)
		fmt.Fprintf(os.Stderr, "\n---  END STRIPPED  ---\n")
	}

	movies, missed, perr := parseMoviesFromBytes(body)
	switch {
	case perr != nil:
		fmt.Fprintf(os.Stderr, "\nПАРСЕР: не смог разобрать JSON: %v\n", perr)
	case missed:
		fmt.Fprintf(os.Stderr, "\nПАРСЕР: ответ распарсился, но movies=null/[]/{} (либо ID не существует, либо имя поля в обёртке другое)\n")
	default:
		fmt.Fprintf(os.Stderr, "\nПАРСЕР: ОК — фильмов %d\n", len(movies))
		for i, m := range movies {
			if i >= 3 {
				fmt.Fprintf(os.Stderr, "  ... и ещё %d\n", len(movies)-3)
				break
			}
			fmt.Fprintf(os.Stderr, "  [%d] id=%d name=%q year=%d files=%d slug=%q\n",
				i, m.MovieID, m.Name, m.Year, len(m.Files), m.Slug)
		}
	}
}
