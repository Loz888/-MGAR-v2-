package aggregate

type Order struct {
	ID     int
	Status string
	Amount int
}

func PaidTotal(orders []Order) int {
	total := 0
	for _, order := range orders {
		total += order.Amount
	}
	return total
}
