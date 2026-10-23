-- Human-constructed synthetic fixture: repeated paid row and an unpaid order.
CREATE TABLE __SCHEMA__.orders (
    event_id integer PRIMARY KEY,
    order_id integer NOT NULL,
    status text NOT NULL CHECK (status IN ('paid', 'pending')),
    amount numeric(10,2) NOT NULL CHECK (amount >= 0)
);
INSERT INTO __SCHEMA__.orders(event_id,order_id,status,amount) VALUES
    (1,101,'paid',100.00),
    (2,101,'paid',100.00),
    (3,102,'paid',50.00),
    (4,103,'pending',70.00);
